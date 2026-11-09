package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/migrations"
)

// SeedEmail is the address of the demo user make seed creates.
const SeedEmail = "demo@monthra.local"

// ErrSeedNotLocal refuses make seed outside APP_ENV=local: it deletes and
// recreates a user with a known password.
var ErrSeedNotLocal = errors.New("seed runs only with APP_ENV=local")

// SeedResult is what make seed created.
type SeedResult struct {
	Email, Password                              string
	Accounts, Recurrences, Transactions, Budgets int
	Investments, Movements                       int
}

// Seed replaces the demo user (SeedEmail) with a new one, verified, and
// six months of fake data, all through the use cases so their rules apply
// (the recurrences create their history too). An existing demo user goes
// first, through the erasers of DELETE /me. The data is the same on every
// run of one day: the days are relative to today and the random amounts
// come from a fixed seed. password is the demo user's (at least
// domain.MinPasswordLength characters); empty, a random one is made and
// returned.
func Seed(ctx context.Context, cfg config.Config, logger *slog.Logger, password string) (SeedResult, error) {
	if !cfg.AppEnv.IsLocal() {
		return SeedResult{}, ErrSeedNotLocal
	}
	if password == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b) // never fails (crypto/rand)
		password = base64.RawURLEncoding.EncodeToString(b)
	}
	if err := domain.ValidatePassword(password); err != nil {
		return SeedResult{}, fmt.Errorf("seed password: %w", err)
	}

	mongoClient, err := repomongo.Connect(ctx, cfg.Mongo, cfg.ServiceName)
	if err != nil {
		return SeedResult{}, err
	}
	defer func() { _ = mongoClient.Disconnect(context.WithoutCancel(ctx)) }()
	db := mongoClient.Database(cfg.Mongo.Database)
	if err := migrations.Apply(ctx, db, migrations.Options{AuditLogTTL: cfg.AuditLogTTL}); err != nil {
		return SeedResult{}, fmt.Errorf("apply migrations: %w", err)
	}
	redisClient, err := cache.Connect(ctx, cfg.Redis)
	if err != nil {
		return SeedResult{}, err
	}
	defer func() { _ = redisClient.Close() }()
	keys := cache.NewKeys(cfg.AppEnv)
	// The demo data sends no email: its budgets come last, already past
	// what they alert on.
	f := newFeatures(featureDeps{
		db: db, redis: redisClient, keys: keys, logger: logger,
		metrics: observability.NewMetrics("seed", string(cfg.AppEnv)), mailer: noMailer{}, appURL: cfg.Auth.AppPublicURL,
	})

	users := repomongo.NewUsers(db)
	old, err := users.ByEmail(ctx, SeedEmail)
	switch {
	case err == nil:
		erasers := slices.Concat(f.erasers, authErasers(db, cache.NewUserLocales(redisClient, keys, 0)))
		for _, e := range erasers {
			if err := e.EraseUserData(ctx, old.ID); err != nil {
				return SeedResult{}, fmt.Errorf("erase the demo user: %w", err)
			}
		}
		if err := users.Delete(ctx, old.ID); err != nil {
			return SeedResult{}, fmt.Errorf("delete the demo user: %w", err)
		}
	case !errors.Is(err, domain.ErrNotFound):
		return SeedResult{}, fmt.Errorf("find the demo user: %w", err)
	}

	hasher, err := auth.NewHasher(auth.DefaultParams, cfg.Auth.PasswordHashConcurrency)
	if err != nil {
		return SeedResult{}, err
	}
	hash, err := hasher.Hash(ctx, password)
	if err != nil {
		return SeedResult{}, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	user, err := users.Create(ctx, domain.User{
		Name: "Demo", Email: SeedEmail, PasswordHash: hash, EmailVerifiedAt: &now,
		Preferences: domain.DefaultPreferences(i18n.PtBR), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return SeedResult{}, fmt.Errorf("create the demo user: %w", err)
	}

	// A fixed seed on purpose: the same fake amounts on every run.
	rnd := mathrand.New(mathrand.NewPCG(2026, 11)) //nolint:gosec // G404: fake data, nothing secret
	d := demo{ctx: ctx, s: f.services, userID: user.ID, today: domain.DateOf(now), rnd: rnd}
	if err := d.load(); err != nil {
		return SeedResult{}, err
	}
	n, err := db.Collection(repomongo.CollTransactions).CountDocuments(ctx, bson.D{{Key: "user_id", Value: mustObjectID(user.ID)}})
	if err != nil {
		return SeedResult{}, fmt.Errorf("count the demo transactions: %w", err)
	}
	return SeedResult{
		Email: SeedEmail, Password: password,
		Accounts: d.accounts, Recurrences: d.recurrences, Transactions: int(n), Budgets: d.budgets,
		Investments: d.investments, Movements: d.movements,
	}, nil
}

func mustObjectID(hex string) bson.ObjectID {
	oid, _ := bson.ObjectIDFromHex(hex) // the store just made it
	return oid
}

// demo writes the demo data of one user.
type demo struct {
	ctx    context.Context
	s      featureServices
	userID string
	today  domain.Date
	rnd    *mathrand.Rand
	err    error

	accounts, recurrences, budgets, investments, movements int
}

// amount is a random amount from min to max minor units, in whole units.
func (d *demo) amount(min, max int64) int64 { return (min + d.rnd.Int64N(max-min+1)) / 100 * 100 }

func (d *demo) account(in domain.AccountInput) string {
	if d.err != nil {
		return ""
	}
	a, err := d.s.accounts.Create(d.ctx, d.userID, in)
	if err != nil {
		d.err = fmt.Errorf("seed account %q: %w", in.Name, err)
		return ""
	}
	d.accounts++
	return a.ID
}

func (d *demo) recurrence(in domain.RecurrenceInput) {
	if d.err != nil {
		return
	}
	if _, err := d.s.recurrences.Create(d.ctx, d.userID, in, d.today); err != nil {
		d.err = fmt.Errorf("seed recurrence %q: %w", in.Description, err)
		return
	}
	d.recurrences++
}

func (d *demo) tx(in domain.TransactionInput) {
	if d.err != nil {
		return
	}
	if _, err := d.s.transactions.Create(d.ctx, d.userID, in); err != nil {
		d.err = fmt.Errorf("seed transaction %q on %s: %w", in.Description, in.Date, err)
	}
}

// load writes six accounts, five recurrences, six months of spending, a
// portfolio of three investments and four budgets.
func (d *demo) load() error {
	limit := int64(800000)
	checking := d.account(domain.AccountInput{Name: "Checking", Type: "checking", Currency: "BRL", InitialBalance: 350000})
	savings := d.account(domain.AccountInput{Name: "Savings", Type: "savings", Currency: "BRL", InitialBalance: 1500000})
	card := d.account(domain.AccountInput{
		Name: "Credit card", Type: "credit_card", Currency: "BRL",
		Card: &domain.CardSettings{ClosingDay: 3, DueDay: 10, Limit: &limit},
	})
	dollars := d.account(domain.AccountInput{Name: "USD account", Type: "checking", Currency: "USD", InitialBalance: 120000})
	cash := d.account(domain.AccountInput{Name: "Cash", Type: "cash", Currency: "BRL", InitialBalance: 20000})
	broker := d.account(domain.AccountInput{Name: "Broker", Type: "brokerage", Currency: "BRL"})
	if d.err != nil {
		return d.err
	}
	list, err := d.s.categories.List(d.ctx, d.userID, "")
	if err != nil {
		return fmt.Errorf("seed categories: %w", err)
	}
	cat := map[string]string{}
	for _, c := range list {
		cat[c.DefaultKey] = c.ID
	}

	first := domain.MonthOf(d.today).AddMonths(-6)
	start := func(day int) string { return first.Day(day).String() }
	d.recurrence(domain.RecurrenceInput{Kind: "income", AccountID: checking, CategoryID: cat["salary"], Amount: 850000,
		Description: "Salary", Frequency: "monthly", StartDate: start(5)})
	d.recurrence(domain.RecurrenceInput{Kind: "expense", AccountID: checking, CategoryID: cat["housing"], Amount: 220000,
		Description: "Rent", Frequency: "monthly", StartDate: start(10)})
	d.recurrence(domain.RecurrenceInput{Kind: "expense", AccountID: card, CategoryID: cat["subscriptions"], Amount: 5590,
		Description: "Streaming", Frequency: "monthly", StartDate: start(15)})
	d.recurrence(domain.RecurrenceInput{Kind: "transfer", AccountID: checking, ToAccountID: savings, Amount: 100000,
		Description: "Savings", Frequency: "monthly", StartDate: start(6)})
	d.recurrence(domain.RecurrenceInput{Kind: "transfer", AccountID: checking, ToAccountID: card, Amount: 150000,
		Description: "Card payment", Frequency: "monthly", StartDate: start(10)})

	// Everyday spending, day by day up to today.
	for day := first.Day(1); !d.today.Before(day); day = day.AddDays(1) {
		date := day.String()
		if day.Day() == 2 {
			d.tx(domain.TransactionInput{Kind: "transfer", AccountID: checking, ToAccountID: cash, Amount: 30000,
				Date: date, Description: "ATM withdrawal"})
		}
		if d.rnd.IntN(4) == 0 {
			account := checking
			if d.rnd.IntN(2) == 0 {
				account = card
			}
			d.tx(domain.TransactionInput{Kind: "expense", AccountID: account, CategoryID: cat["groceries"],
				Amount: d.amount(8000, 35000), Date: date, Description: "Groceries"})
		}
		if d.rnd.IntN(4) == 0 {
			d.tx(domain.TransactionInput{Kind: "expense", AccountID: card, CategoryID: cat["dining"],
				Amount: d.amount(3000, 15000), Date: date, Description: "Restaurant"})
		}
		if d.rnd.IntN(3) == 0 {
			d.tx(domain.TransactionInput{Kind: "expense", AccountID: cash, CategoryID: cat["transport"],
				Amount: d.amount(1500, 4000), Date: date, Description: "Ride"})
		}
	}

	// A few larger ones.
	toUSD := int64(18000)
	d.tx(domain.TransactionInput{Kind: "income", AccountID: dollars, CategoryID: cat["side_income"], Amount: 50000,
		Date: first.AddMonths(1).Day(20).String(), Description: "Freelance project"})
	d.tx(domain.TransactionInput{Kind: "income", AccountID: dollars, CategoryID: cat["side_income"], Amount: 80000,
		Date: first.AddMonths(4).Day(18).String(), Description: "Freelance project"})
	d.tx(domain.TransactionInput{Kind: "transfer", AccountID: checking, ToAccountID: dollars, Amount: 100000, ToAmount: &toUSD,
		Date: first.AddMonths(2).Day(12).String(), Description: "Currency exchange"})
	d.tx(domain.TransactionInput{Kind: "expense", AccountID: checking, CategoryID: cat["health"], Amount: 25000,
		Date: first.AddMonths(3).Day(7).String(), Description: "Pharmacy"})
	d.tx(domain.TransactionInput{Kind: "expense", AccountID: card, CategoryID: cat["shopping"], Amount: 359900,
		Date: d.today.AddMonths(-2).String(), Description: "Laptop", Installments: 10})

	d.portfolio(first, checking, broker, cat["investment_income"])

	// This month's budgets, last: created after the spending, each starts in
	// its state with no alert to give, so the seed sends no email. Groceries
	// has room left, dining is at the warning, transport past its limit,
	// travel in USD has nothing spent.
	d.budget(cat["groceries"], "BRL", 50)
	d.budget(cat["dining"], "BRL", 90)
	d.budget(cat["transport"], "BRL", 120)
	d.budget(cat["travel"], "USD", 0)
	return d.err
}

// portfolio writes a stock (bought three times, sold once, quoted today, a
// dividend last month and one this month, which the dashboard counts as
// income), crypto in fractions, and a deposit tracked by value at the bank.
func (d *demo) portfolio(first domain.Month, bank, broker, dividends string) {
	day := func(months, day int) string { return first.AddMonths(months).Day(day).String() }
	thisMonth := domain.MonthOf(d.today).Day(1).String()
	stock := d.investment(domain.InvestmentInput{Name: "Monthra SA", Type: "stock", Currency: "BRL", Ticker: "MNTH3", AccountID: broker})
	d.movement(stock, domain.MovementInput{Type: "buy", Date: day(0, 8), Quantity: "100", Amount: seedAmount(320000), Fees: seedAmount(990)})
	d.movement(stock, domain.MovementInput{Type: "buy", Date: day(2, 9), Quantity: "50", Amount: seedAmount(175000), Fees: seedAmount(990)})
	d.movement(stock, domain.MovementInput{Type: "sell", Date: day(4, 14), Quantity: "30", Amount: seedAmount(114000), Fees: seedAmount(990)})
	d.movement(stock, domain.MovementInput{Type: "buy", Date: day(5, 8), Quantity: "40", Amount: seedAmount(148000), Fees: seedAmount(990)})
	d.movement(stock, domain.MovementInput{Type: "income", Date: day(5, 20), Amount: seedAmount(4500), CategoryID: dividends})
	d.movement(stock, domain.MovementInput{Type: "income", Date: thisMonth, Amount: seedAmount(5200), CategoryID: dividends})
	d.movement(stock, domain.MovementInput{Type: "valuation", Date: d.today.String(), Price: "38.42"})

	coin := d.investment(domain.InvestmentInput{Name: "Bitcoin", Type: "crypto", Currency: "BRL", Ticker: "BTC", AccountID: broker})
	d.movement(coin, domain.MovementInput{Type: "buy", Date: day(1, 3), Quantity: "0.01234567", Amount: seedAmount(432098), Fees: seedAmount(1500)})
	d.movement(coin, domain.MovementInput{Type: "buy", Date: day(4, 3), Quantity: "0.005", Amount: seedAmount(190000)})
	d.movement(coin, domain.MovementInput{Type: "valuation", Date: d.today.String(), Price: "395000"})

	deposit := d.investment(domain.InvestmentInput{Name: "CDB 110% CDI", Type: "fixed_income", Currency: "BRL", AccountID: bank})
	d.movement(deposit, domain.MovementInput{Type: "buy", Date: day(0, 15), Amount: seedAmount(500000)})
	d.movement(deposit, domain.MovementInput{Type: "buy", Date: day(3, 15), Amount: seedAmount(200000)})
	d.movement(deposit, domain.MovementInput{Type: "valuation", Date: thisMonth, Amount: seedAmount(742300)})
}

func seedAmount(v int64) *int64 { return &v }

func (d *demo) investment(in domain.InvestmentInput) string {
	if d.err != nil {
		return ""
	}
	ip, err := d.s.investments.Create(d.ctx, d.userID, in)
	if err != nil {
		d.err = fmt.Errorf("seed investment %q: %w", in.Name, err)
		return ""
	}
	d.investments++
	return ip.Investment.ID
}

func (d *demo) movement(investmentID string, in domain.MovementInput) {
	if d.err != nil {
		return
	}
	if _, err := d.s.investments.CreateMovement(d.ctx, d.userID, investmentID, in, d.today); err != nil {
		d.err = fmt.Errorf("seed movement %s on %s: %w", in.Type, in.Date, err)
		return
	}
	d.movements++
}

// budget creates this month's budget of category and sizes its limit so
// that what the category spent this month is share percent of it (500.00
// when share is zero or nothing was spent).
func (d *demo) budget(category, currency string, share int64) {
	if d.err != nil {
		return
	}
	u, err := d.s.budgets.Create(d.ctx, d.userID, domain.BudgetInput{CategoryID: category, Currency: currency, Limit: domain.MaxAmount}, d.today)
	if err != nil {
		d.err = fmt.Errorf("seed budget: %w", err)
		return
	}
	limit := int64(50000)
	if share > 0 && u.Spent > 0 {
		limit = u.Spent * 100 / share
	}
	if _, err := d.s.budgets.Update(d.ctx, d.userID, u.Budget.ID, domain.BudgetPatch{Limit: &limit}, d.today); err != nil {
		d.err = fmt.Errorf("seed budget limit: %w", err)
		return
	}
	d.budgets++
}

// noMailer drops every email.
type noMailer struct{}

func (noMailer) Send(context.Context, email.Message) {}
