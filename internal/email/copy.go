package email

import "github.com/NickeAlves/monthra-server/internal/i18n"

// copyText is the translated text of one template. "{minutes}" in a line is
// replaced by the link lifetime. A template without Action has no button.
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
	TemplateResetPassword: {
		i18n.PtBR: {
			Subject: "Redefina sua senha no Monthra",
			Heading: "Redefinir senha",
			Lines: []string{
				"Recebemos um pedido para redefinir a senha da sua conta no Monthra.",
				"Para escolher uma nova senha, use o botão abaixo. O link vale por {minutes} minutos e só pode ser usado uma vez. Ao trocar a senha, todas as sessões abertas são encerradas.",
			},
			Action: "Redefinir senha",
			Footer: "Se você não pediu isso, ignore esta mensagem. Sua senha continua a mesma.",
		},
		i18n.EsES: {
			Subject: "Restablece tu contraseña en Monthra",
			Heading: "Restablecer contraseña",
			Lines: []string{
				"Recibimos una solicitud para restablecer la contraseña de tu cuenta en Monthra.",
				"Para elegir una nueva contraseña, usa el botón de abajo. El enlace es válido durante {minutes} minutos y solo se puede usar una vez. Al cambiar la contraseña, se cierran todas las sesiones abiertas.",
			},
			Action: "Restablecer contraseña",
			Footer: "Si no lo solicitaste, ignora este mensaje. Tu contraseña sigue siendo la misma.",
		},
		i18n.EnUS: {
			Subject: "Reset your Monthra password",
			Heading: "Reset your password",
			Lines: []string{
				"We received a request to reset the password of your Monthra account.",
				"To choose a new password, use the button below. The link is valid for {minutes} minutes and can be used only once. Changing the password signs out every open session.",
			},
			Action: "Reset password",
			Footer: "If you did not ask for this, ignore this message. Your password stays the same.",
		},
	},
	TemplatePasswordChanged: {
		i18n.PtBR: {
			Subject: "Sua senha do Monthra foi alterada",
			Heading: "Senha alterada",
			Lines: []string{
				"A senha da sua conta no Monthra acabou de ser alterada, e todas as sessões abertas foram encerradas.",
				"Se não foi você, redefina a senha agora pelo botão abaixo.",
			},
			Action: "Redefinir senha",
			Footer: "Se foi você, não precisa fazer mais nada.",
		},
		i18n.EsES: {
			Subject: "Se cambió tu contraseña de Monthra",
			Heading: "Contraseña cambiada",
			Lines: []string{
				"La contraseña de tu cuenta en Monthra acaba de cambiar y se cerraron todas las sesiones abiertas.",
				"Si no fuiste tú, restablece la contraseña ahora con el botón de abajo.",
			},
			Action: "Restablecer contraseña",
			Footer: "Si fuiste tú, no necesitas hacer nada más.",
		},
		i18n.EnUS: {
			Subject: "Your Monthra password was changed",
			Heading: "Password changed",
			Lines: []string{
				"The password of your Monthra account was just changed, and every open session was signed out.",
				"If it was not you, reset your password now with the button below.",
			},
			Action: "Reset password",
			Footer: "If it was you, there is nothing else to do.",
		},
	},
	TemplateMFAEnabled: {
		i18n.PtBR: {
			Subject: "Verificação em duas etapas ativada no Monthra",
			Heading: "Verificação em duas etapas ativada",
			Lines: []string{
				"A verificação em duas etapas foi ativada na sua conta no Monthra. A partir de agora, o login também pede um código do seu app autenticador.",
				"Guarde os códigos de recuperação em um lugar seguro: eles são a única forma de entrar se você perder o app.",
				"Se não foi você, redefina a senha agora pelo botão abaixo. Isso encerra todas as sessões abertas.",
			},
			Action: "Redefinir senha",
			Footer: "Se foi você, não precisa fazer mais nada.",
		},
		i18n.EsES: {
			Subject: "Verificación en dos pasos activada en Monthra",
			Heading: "Verificación en dos pasos activada",
			Lines: []string{
				"Se activó la verificación en dos pasos en tu cuenta de Monthra. A partir de ahora, el inicio de sesión también pide un código de tu app de autenticación.",
				"Guarda los códigos de recuperación en un lugar seguro: son la única forma de entrar si pierdes la app.",
				"Si no fuiste tú, restablece la contraseña ahora con el botón de abajo. Eso cierra todas las sesiones abiertas.",
			},
			Action: "Restablecer contraseña",
			Footer: "Si fuiste tú, no necesitas hacer nada más.",
		},
		i18n.EnUS: {
			Subject: "Two-step verification turned on for Monthra",
			Heading: "Two-step verification is on",
			Lines: []string{
				"Two-step verification was turned on for your Monthra account. From now on, signing in also asks for a code from your authenticator app.",
				"Keep your recovery codes somewhere safe: they are the only way in if you lose the app.",
				"If it was not you, reset your password now with the button below. That signs out every open session.",
			},
			Action: "Reset password",
			Footer: "If it was you, there is nothing else to do.",
		},
	},
	TemplateMFADisabled: {
		i18n.PtBR: {
			Subject: "Verificação em duas etapas desativada no Monthra",
			Heading: "Verificação em duas etapas desativada",
			Lines: []string{
				"A verificação em duas etapas foi desativada na sua conta no Monthra. O login volta a pedir só o e-mail e a senha.",
				"Se não foi você, redefina a senha agora pelo botão abaixo e ative a verificação de novo.",
			},
			Action: "Redefinir senha",
			Footer: "Se foi você, não precisa fazer mais nada.",
		},
		i18n.EsES: {
			Subject: "Verificación en dos pasos desactivada en Monthra",
			Heading: "Verificación en dos pasos desactivada",
			Lines: []string{
				"Se desactivó la verificación en dos pasos en tu cuenta de Monthra. El inicio de sesión vuelve a pedir solo el correo y la contraseña.",
				"Si no fuiste tú, restablece la contraseña ahora con el botón de abajo y vuelve a activar la verificación.",
			},
			Action: "Restablecer contraseña",
			Footer: "Si fuiste tú, no necesitas hacer nada más.",
		},
		i18n.EnUS: {
			Subject: "Two-step verification turned off for Monthra",
			Heading: "Two-step verification is off",
			Lines: []string{
				"Two-step verification was turned off for your Monthra account. Signing in asks only for your email and password again.",
				"If it was not you, reset your password now with the button below and turn the verification back on.",
			},
			Action: "Reset password",
			Footer: "If it was you, there is nothing else to do.",
		},
	},
	TemplateAccountDeleted: {
		i18n.PtBR: {
			Subject: "Sua conta no Monthra foi excluída",
			Heading: "Conta excluída",
			Lines: []string{
				"A sua conta no Monthra foi excluída, junto com os dados dela, e todas as sessões foram encerradas.",
				"Só os registros de segurança de acesso ficam guardados, pelo prazo que a lei exige, e depois são apagados.",
			},
			Footer: "Se não foi você, alguém sabia a sua senha: troque-a nos outros serviços em que você usa a mesma.",
		},
		i18n.EsES: {
			Subject: "Tu cuenta de Monthra fue eliminada",
			Heading: "Cuenta eliminada",
			Lines: []string{
				"Tu cuenta de Monthra fue eliminada, junto con sus datos, y se cerraron todas las sesiones.",
				"Solo se conservan los registros de seguridad de acceso, durante el plazo que exige la ley, y luego se borran.",
			},
			Footer: "Si no fuiste tú, alguien conocía tu contraseña: cámbiala en los demás servicios donde uses la misma.",
		},
		i18n.EnUS: {
			Subject: "Your Monthra account was deleted",
			Heading: "Account deleted",
			Lines: []string{
				"Your Monthra account was deleted, along with its data, and every session was signed out.",
				"Only the security access records are kept, for as long as the law requires, and then erased.",
			},
			Footer: "If it was not you, someone knew your password: change it on any other service where you use the same one.",
		},
	},
}
