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
	Email, Password                     string
	Accounts, Recurrences, Transactions int
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
	f := newFeatures(db, redisClient, keys, logger, observability.NewMetrics("seed", string(cfg.AppEnv)))

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
		Accounts: d.accounts, Recurrences: d.recurrences, Transactions: int(n),
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

	accounts, recurrences int
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

// load writes five accounts, five recurrences and six months of spending.
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
	return d.err
}
