package email

import "github.com/NickeAlves/monthra-server/internal/i18n"

// copyText is the translated text of one template. "{minutes}" in a line is
// replaced by the link lifetime.
type copyText struct {
	Subject string
	Heading string
	Lines   []string
	Action  string
	Footer  string
}

// catalog holds every template in every supported locale.
// TestCatalogIsComplete fails when one is missing.
var catalog = map[Template]map[i18n.Locale]copyText{
	TemplateVerifyEmail: {
		i18n.PtBR: {
			Subject: "Confirme seu e-mail no Monthra",
			Heading: "Confirme seu e-mail",
			Lines: []string{
				"Recebemos um cadastro no Monthra com este endereço de e-mail.",
				"Para ativar sua conta, confirme o e-mail pelo botão abaixo. O link vale por {minutes} minutos e só pode ser usado uma vez.",
			},
			Action: "Confirmar e-mail",
			Footer: "Se você não criou uma conta no Monthra, ignore esta mensagem.",
		},
		i18n.EsES: {
			Subject: "Confirma tu correo electrónico en Monthra",
			Heading: "Confirma tu correo electrónico",
			Lines: []string{
				"Recibimos un registro en Monthra con esta dirección de correo.",
				"Para activar tu cuenta, confirma el correo con el botón de abajo. El enlace es válido durante {minutes} minutos y solo se puede usar una vez.",
			},
			Action: "Confirmar correo",
			Footer: "Si no creaste una cuenta en Monthra, ignora este mensaje.",
		},
		i18n.EnUS: {
			Subject: "Confirm your email for Monthra",
			Heading: "Confirm your email",
			Lines: []string{
				"We received a Monthra sign-up with this email address.",
				"To activate your account, confirm your email with the button below. The link is valid for {minutes} minutes and can be used only once.",
			},
			Action: "Confirm email",
			Footer: "If you did not create a Monthra account, you can ignore this message.",
		},
	},
	TemplateRegisterAttempt: {
		i18n.PtBR: {
			Subject: "Tentativa de cadastro no Monthra",
			Heading: "Você já tem uma conta",
			Lines: []string{
				"Alguém tentou criar uma conta no Monthra com este e-mail, mas ele já está cadastrado.",
				"Se foi você, entre com a sua senha. Se não lembra dela, você pode criar uma nova.",
			},
			Action: "Recuperar senha",
			Footer: "Se não foi você, ignore esta mensagem. Nada mudou na sua conta.",
		},
		i18n.EsES: {
			Subject: "Intento de registro en Monthra",
			Heading: "Ya tienes una cuenta",
			Lines: []string{
				"Alguien intentó crear una cuenta en Monthra con este correo, pero ya está registrado.",
				"Si fuiste tú, inicia sesión con tu contraseña. Si no la recuerdas, puedes crear una nueva.",
			},
			Action: "Recuperar contraseña",
			Footer: "Si no fuiste tú, ignora este mensaje. Nada cambió en tu cuenta.",
		},
		i18n.EnUS: {
			Subject: "Sign-up attempt on Monthra",
			Heading: "You already have an account",
			Lines: []string{
				"Someone tried to create a Monthra account with this email, but it is already registered.",
				"If it was you, sign in with your password. If you do not remember it, you can set a new one.",
			},
			Action: "Reset password",
			Footer: "If it was not you, ignore this message. Nothing changed in your account.",
		},
	},
}
