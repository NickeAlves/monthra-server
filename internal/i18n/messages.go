package i18n

// Key identifies a translated message. Error keys double as the stable
// machine-readable `code` of problem+json responses.
type Key string

// Error message keys.
const (
	ErrBadRequest         Key = "bad_request"
	ErrNotFound           Key = "not_found"
	ErrMethodNotAllowed   Key = "method_not_allowed"
	ErrPayloadTooLarge    Key = "payload_too_large"
	ErrInternal           Key = "internal_error"
	ErrServiceUnavailable Key = "service_unavailable"

	ErrRateLimited          Key = "rate_limited"
	ErrOriginNotAllowed     Key = "origin_not_allowed"
	ErrCSRFInvalid          Key = "csrf_invalid"
	ErrAmbiguousCredentials Key = "ambiguous_credentials"

	ErrUnsupportedMediaType Key = "unsupported_media_type"
	ErrValidationFailed     Key = "validation_failed"
	ErrUnauthorized         Key = "unauthorized"
	ErrInvalidCredentials   Key = "invalid_credentials" //nolint:gosec // G101: an error code, not a credential
	ErrEmailNotVerified     Key = "email_not_verified"
	ErrTokenInvalid         Key = "token_invalid"
	ErrRefreshConflict      Key = "refresh_conflict"
	ErrCaptchaRequired      Key = "captcha_required"
	ErrCaptchaInvalid       Key = "captcha_invalid"
	ErrPasswordIncorrect    Key = "password_incorrect"
	ErrMFACodeInvalid       Key = "mfa_code_invalid"
	ErrMFAChallengeInvalid  Key = "mfa_challenge_invalid"
	ErrMFAAlreadyEnabled    Key = "mfa_already_enabled"
	ErrMFANotEnabled        Key = "mfa_not_enabled"
	ErrMFASetupRequired     Key = "mfa_setup_required"

	ErrLimitReached  Key = "limit_reached"
	ErrAccountInUse  Key = "account_in_use"
	ErrCategoryInUse Key = "category_in_use"
)

// Message is a translated title and detail.
type Message struct {
	Title  string
	Detail string
}

// catalog holds every message in every supported locale. TestCatalogIsComplete
// fails when a key misses a locale.
var catalog = map[Key]map[Locale]Message{
	ErrBadRequest: {
		PtBR: {"Requisição inválida", "A requisição está malformada ou tem parâmetros inválidos."},
		EsES: {"Solicitud no válida", "La solicitud está mal formada o tiene parámetros no válidos."},
		EnUS: {"Bad request", "The request is malformed or has invalid parameters."},
	},
	ErrNotFound: {
		PtBR: {"Não encontrado", "O recurso solicitado não existe."},
		EsES: {"No encontrado", "El recurso solicitado no existe."},
		EnUS: {"Not found", "The requested resource does not exist."},
	},
	ErrMethodNotAllowed: {
		PtBR: {"Método não permitido", "Este recurso não aceita o método HTTP usado."},
		EsES: {"Método no permitido", "Este recurso no acepta el método HTTP utilizado."},
		EnUS: {"Method not allowed", "This resource does not accept the HTTP method used."},
	},
	ErrPayloadTooLarge: {
		PtBR: {"Corpo muito grande", "O corpo da requisição excede o tamanho máximo permitido."},
		EsES: {"Cuerpo demasiado grande", "El cuerpo de la solicitud supera el tamaño máximo permitido."},
		EnUS: {"Payload too large", "The request body exceeds the maximum allowed size."},
	},
	ErrInternal: {
		PtBR: {"Erro interno", "Ocorreu um erro inesperado. Tente novamente mais tarde."},
		EsES: {"Error interno", "Se produjo un error inesperado. Inténtalo de nuevo más tarde."},
		EnUS: {"Internal error", "An unexpected error occurred. Please try again later."},
	},
	ErrServiceUnavailable: {
		PtBR: {"Serviço indisponível", "O serviço está temporariamente indisponível. Tente novamente em instantes."},
		EsES: {"Servicio no disponible", "El servicio no está disponible temporalmente. Inténtalo de nuevo en unos momentos."},
		EnUS: {"Service unavailable", "The service is temporarily unavailable. Please try again shortly."},
	},
	ErrRateLimited: {
		PtBR: {"Muitas requisições", "Você fez muitas requisições em pouco tempo. Aguarde e tente novamente."},
		EsES: {"Demasiadas solicitudes", "Has hecho demasiadas solicitudes en poco tiempo. Espera e inténtalo de nuevo."},
		EnUS: {"Too many requests", "You made too many requests in a short time. Please wait and try again."},
	},
	ErrOriginNotAllowed: {
		PtBR: {"Origem não permitida", "Esta origem não tem permissão para chamar a API."},
		EsES: {"Origen no permitido", "Este origen no tiene permiso para llamar a la API."},
		EnUS: {"Origin not allowed", "This origin is not allowed to call the API."},
	},
	ErrCSRFInvalid: {
		PtBR: {"Token CSRF inválido", "O token CSRF está ausente ou é inválido. Recarregue a página e tente novamente."},
		EsES: {"Token CSRF no válido", "El token CSRF falta o no es válido. Recarga la página e inténtalo de nuevo."},
		EnUS: {"Invalid CSRF token", "The CSRF token is missing or invalid. Reload the page and try again."},
	},
	ErrAmbiguousCredentials: {
		PtBR: {"Credenciais ambíguas", "Envie o token pelo cabeçalho Authorization ou por cookie, nunca pelos dois."},
		EsES: {"Credenciales ambiguas", "Envía el token por el encabezado Authorization o por cookie, nunca por ambos."},
		EnUS: {"Ambiguous credentials", "Send the token in the Authorization header or in a cookie, never both."},
	},
	ErrUnsupportedMediaType: {
		PtBR: {"Tipo de conteúdo não suportado", "Envie o corpo como application/json."},
		EsES: {"Tipo de contenido no admitido", "Envía el cuerpo como application/json."},
		EnUS: {"Unsupported media type", "Send the body as application/json."},
	},
	ErrValidationFailed: {
		PtBR: {"Dados inválidos", "Um ou mais campos são inválidos. Veja a lista de erros."},
		EsES: {"Datos no válidos", "Uno o más campos no son válidos. Consulta la lista de errores."},
		EnUS: {"Invalid data", "One or more fields are invalid. See the list of errors."},
	},
	ErrUnauthorized: {
		PtBR: {"Não autenticado", "Sua sessão é inválida ou expirou. Entre novamente."},
		EsES: {"No autenticado", "Tu sesión no es válida o ha caducado. Inicia sesión de nuevo."},
		EnUS: {"Unauthorized", "Your session is invalid or has expired. Please sign in again."},
	},
	ErrInvalidCredentials: {
		PtBR: {"Credenciais inválidas", "E-mail ou senha incorretos."},
		EsES: {"Credenciales no válidas", "Correo electrónico o contraseña incorrectos."},
		EnUS: {"Invalid credentials", "Incorrect email or password."},
	},
	ErrEmailNotVerified: {
		PtBR: {"E-mail não confirmado", "Confirme seu e-mail pelo link que enviamos antes de entrar."},
		EsES: {"Correo no confirmado", "Confirma tu correo con el enlace que te enviamos antes de iniciar sesión."},
		EnUS: {"Email not verified", "Confirm your email with the link we sent before signing in."},
	},
	ErrTokenInvalid: {
		PtBR: {"Link inválido ou expirado", "Este link não é mais válido. Peça um novo."},
		EsES: {"Enlace no válido o caducado", "Este enlace ya no es válido. Solicita uno nuevo."},
		EnUS: {"Invalid or expired link", "This link is no longer valid. Request a new one."},
	},
	ErrRefreshConflict: {
		PtBR: {"Sessão já renovada", "Outra requisição acabou de renovar a sessão. Repita a requisição original."},
		EsES: {"Sesión ya renovada", "Otra solicitud acaba de renovar la sesión. Repite la solicitud original."},
		EnUS: {"Session already renewed", "Another request just renewed the session. Retry the original request."},
	},
	ErrCaptchaRequired: {
		PtBR: {"Verificação necessária", "Confirme que você não é um robô e tente novamente."},
		EsES: {"Verificación necesaria", "Confirma que no eres un robot e inténtalo de nuevo."},
		EnUS: {"Verification required", "Confirm that you are not a robot and try again."},
	},
	ErrCaptchaInvalid: {
		PtBR: {"Verificação inválida", "Não foi possível confirmar a verificação. Tente novamente."},
		EsES: {"Verificación no válida", "No se pudo confirmar la verificación. Inténtalo de nuevo."},
		EnUS: {"Invalid verification", "The verification could not be confirmed. Please try again."},
	},
	ErrPasswordIncorrect: {
		PtBR: {"Senha incorreta", "Confirme a sua senha atual para fazer esta alteração."},
		EsES: {"Contraseña incorrecta", "Confirma tu contraseña actual para hacer este cambio."},
		EnUS: {"Incorrect password", "Confirm your current password to make this change."},
	},
	ErrMFACodeInvalid: {
		PtBR: {"Código inválido", "O código está incorreto ou já foi usado. Use o código atual do app autenticador ou um código de recuperação."},
		EsES: {"Código no válido", "El código es incorrecto o ya se usó. Usa el código actual de la app de autenticación o un código de recuperación."},
		EnUS: {"Invalid code", "The code is wrong or was already used. Use the current code from your authenticator app or a recovery code."},
	},
	ErrMFAChallengeInvalid: {
		PtBR: {"Login expirado", "Esta etapa do login expirou ou teve tentativas demais. Entre novamente com e-mail e senha."},
		EsES: {"Inicio de sesión caducado", "Este paso del inicio de sesión caducó o tuvo demasiados intentos. Inicia sesión de nuevo con correo y contraseña."},
		EnUS: {"Sign-in expired", "This sign-in step expired or had too many attempts. Sign in again with your email and password."},
	},
	ErrMFAAlreadyEnabled: {
		PtBR: {"Verificação em duas etapas já ativada", "Desative a verificação atual antes de configurar outra."},
		EsES: {"Verificación en dos pasos ya activada", "Desactiva la verificación actual antes de configurar otra."},
		EnUS: {"Two-step verification already on", "Turn off the current verification before setting up another one."},
	},
	ErrMFANotEnabled: {
		PtBR: {"Verificação em duas etapas desativada", "A verificação em duas etapas não está ativada nesta conta."},
		EsES: {"Verificación en dos pasos desactivada", "La verificación en dos pasos no está activada en esta cuenta."},
		EnUS: {"Two-step verification is off", "Two-step verification is not turned on for this account."},
	},
	ErrMFASetupRequired: {
		PtBR: {"Configuração expirada", "Comece a configuração da verificação em duas etapas de novo."},
		EsES: {"Configuración caducada", "Vuelve a empezar la configuración de la verificación en dos pasos."},
		EnUS: {"Setup expired", "Start the two-step verification setup again."},
	},
	ErrLimitReached: {
		PtBR: {"Limite atingido", "Você já tem o número máximo de itens deste tipo. Apague ou arquive algum antes de criar outro."},
		EsES: {"Límite alcanzado", "Ya tienes el número máximo de elementos de este tipo. Elimina o archiva alguno antes de crear otro."},
		EnUS: {"Limit reached", "You already have the maximum number of items of this kind. Delete or archive one before creating another."},
	},
	ErrAccountInUse: {
		PtBR: {"Conta com lançamentos", "Esta conta tem lançamentos e não pode ser apagada. Arquive a conta para escondê-la mantendo o histórico."},
		EsES: {"Cuenta con movimientos", "Esta cuenta tiene movimientos y no se puede eliminar. Archívala para ocultarla y conservar el historial."},
		EnUS: {"Account in use", "This account has transactions and cannot be deleted. Archive it to hide it and keep its history."},
	},
	ErrCategoryInUse: {
		PtBR: {"Categoria com lançamentos", "Esta categoria tem lançamentos. Escolha outra categoria do mesmo tipo para movê-los antes de apagar."},
		EsES: {"Categoría con movimientos", "Esta categoría tiene movimientos. Elige otra categoría del mismo tipo para moverlos antes de eliminarla."},
		EnUS: {"Category in use", "This category has transactions. Pick another category of the same kind to move them to before deleting it."},
	},
}

// T returns the message for key in locale, falling back to the default locale
// and finally to the key itself so a missing translation never breaks a response.
func T(l Locale, key Key) Message {
	if byLocale, ok := catalog[key]; ok {
		if m, ok := byLocale[l]; ok {
			return m
		}
		if m, ok := byLocale[Default]; ok {
			return m
		}
	}
	return Message{Title: string(key)}
}

// Keys returns every key in the catalog.
func Keys() []Key {
	keys := make([]Key, 0, len(catalog))
	for k := range catalog {
		keys = append(keys, k)
	}
	return keys
}
