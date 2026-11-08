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
