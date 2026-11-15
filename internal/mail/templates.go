package mail

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

// Kind selects a transactional template.
type Kind string

// Templates.
const (
	KindTest          Kind = "test"
	KindVerifyEmail   Kind = "verify_email"
	KindLoginCode     Kind = "login_code"
	KindPasswordReset Kind = "password_reset"
)

// Locales with templates; anything else falls back to DefaultLocale.
const (
	LocaleEN      = "en"
	LocaleRU      = "ru"
	LocaleDE      = "de"
	DefaultLocale = LocaleEN
)

// NormalizeLocale maps "ru-RU", "DE" etc. to a supported locale ("" when
// unsupported).
func NormalizeLocale(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i]
	}
	switch s {
	case LocaleEN, LocaleRU, LocaleDE:
		return s
	}
	return ""
}

// Data fills a template.
type Data struct {
	// Title is the panel name (branding title).
	Title    string
	Username string
	// Code is the one-time code (verify_email, login_code).
	Code string
	// Link is the password reset link.
	Link string
	// Minutes is the validity of the code or link.
	Minutes int
	// IP is the client address that triggered the e-mail.
	IP string
}

type tmpl struct {
	subject string
	// lines are paragraphs; {{.X}} placeholders. The HTML part escapes data.
	lines []string
}

var templates = map[string]map[Kind]tmpl{
	LocaleEN: {
		KindTest: {"Test e-mail from {{.Title}}", []string{
			"This is a test message from {{.Title}}.",
			"If you received it, outgoing e-mail is configured correctly.",
		}},
		KindVerifyEmail: {"{{.Code}} is your {{.Title}} verification code", []string{
			"Hello {{.Username}},",
			"Use this code to verify your e-mail address:",
			"{{code .Code}}",
			"The code expires in {{.Minutes}} minutes. If you did not request it, ignore this message.",
		}},
		KindLoginCode: {"{{.Code}} is your {{.Title}} sign-in code", []string{
			"Hello {{.Username}},",
			"Your sign-in code:",
			"{{code .Code}}",
			"The code expires in {{.Minutes}} minutes.{{if .IP}} Requested from {{.IP}}.{{end}}",
			"If this was not you, change your password: someone knows it.",
		}},
		KindPasswordReset: {"Reset your {{.Title}} password", []string{
			"Hello {{.Username}},",
			"Someone (hopefully you) asked to reset the password of your account{{if .IP}} from {{.IP}}{{end}}. Open this link to choose a new password:",
			"{{link .Link}}",
			"The link works once and expires in {{.Minutes}} minutes. All sessions will be signed out after the reset.",
			"If you did not ask for this, ignore this message: your password stays the same.",
		}},
	},
	LocaleRU: {
		KindTest: {"Тестовое письмо от {{.Title}}", []string{
			"Это тестовое письмо от {{.Title}}.",
			"Если вы его получили, отправка почты настроена правильно.",
		}},
		KindVerifyEmail: {"{{.Code}} — код подтверждения {{.Title}}", []string{
			"Здравствуйте, {{.Username}}!",
			"Код для подтверждения адреса электронной почты:",
			"{{code .Code}}",
			"Код действует {{.Minutes}} минут. Если вы его не запрашивали, просто проигнорируйте это письмо.",
		}},
		KindLoginCode: {"{{.Code}} — код входа в {{.Title}}", []string{
			"Здравствуйте, {{.Username}}!",
			"Ваш код для входа:",
			"{{code .Code}}",
			"Код действует {{.Minutes}} минут.{{if .IP}} Запрос с адреса {{.IP}}.{{end}}",
			"Если это были не вы, смените пароль: он известен кому-то ещё.",
		}},
		KindPasswordReset: {"Сброс пароля {{.Title}}", []string{
			"Здравствуйте, {{.Username}}!",
			"Кто-то (надеемся, вы) запросил сброс пароля вашей учётной записи{{if .IP}} с адреса {{.IP}}{{end}}. Откройте ссылку, чтобы задать новый пароль:",
			"{{link .Link}}",
			"Ссылка одноразовая и действует {{.Minutes}} минут. После сброса все сеансы будут завершены.",
			"Если вы не запрашивали сброс, проигнорируйте письмо: пароль останется прежним.",
		}},
	},
	LocaleDE: {
		KindTest: {"Test-E-Mail von {{.Title}}", []string{
			"Dies ist eine Testnachricht von {{.Title}}.",
			"Wenn Sie sie erhalten haben, ist der E-Mail-Versand richtig eingerichtet.",
		}},
		KindVerifyEmail: {"{{.Code}} ist Ihr Bestätigungscode für {{.Title}}", []string{
			"Hallo {{.Username}},",
			"mit diesem Code bestätigen Sie Ihre E-Mail-Adresse:",
			"{{code .Code}}",
			"Der Code ist {{.Minutes}} Minuten gültig. Wenn Sie ihn nicht angefordert haben, ignorieren Sie diese Nachricht.",
		}},
		KindLoginCode: {"{{.Code}} ist Ihr Anmeldecode für {{.Title}}", []string{
			"Hallo {{.Username}},",
			"Ihr Anmeldecode:",
			"{{code .Code}}",
			"Der Code ist {{.Minutes}} Minuten gültig.{{if .IP}} Angefordert von {{.IP}}.{{end}}",
			"Waren Sie das nicht, ändern Sie Ihr Passwort: Jemand kennt es.",
		}},
		KindPasswordReset: {"Passwort für {{.Title}} zurücksetzen", []string{
			"Hallo {{.Username}},",
			"jemand (hoffentlich Sie) möchte das Passwort Ihres Kontos zurücksetzen{{if .IP}}, angefordert von {{.IP}}{{end}}. Über diesen Link wählen Sie ein neues Passwort:",
			"{{link .Link}}",
			"Der Link funktioniert einmal und ist {{.Minutes}} Minuten gültig. Nach dem Zurücksetzen werden alle Sitzungen abgemeldet.",
			"Haben Sie das nicht angefordert, ignorieren Sie diese Nachricht: Ihr Passwort bleibt unverändert.",
		}},
	},
}

var textFuncs = texttemplate.FuncMap{
	"code": func(s string) string { return "    " + s },
	"link": func(s string) string { return s },
}

var htmlFuncs = htmltemplate.FuncMap{
	"code": func(s string) htmltemplate.HTML {
		return htmltemplate.HTML(`<span style="font-size:24px;font-weight:bold;letter-spacing:4px;font-family:monospace">` +
			htmltemplate.HTMLEscapeString(s) + `</span>`) //nolint:gosec // escaped
	},
	"link": func(s string) htmltemplate.HTML {
		e := htmltemplate.HTMLEscapeString(s)
		return htmltemplate.HTML(`<a href="` + e + `">` + e + `</a>`) //nolint:gosec // escaped
	},
}

// Render builds the subject and bodies of kind in locale (fallback: en).
func Render(kind Kind, locale string, d Data) (Message, error) {
	set := templates[NormalizeLocale(locale)]
	if set == nil {
		set = templates[DefaultLocale]
	}
	t, ok := set[kind]
	if !ok {
		return Message{}, fmt.Errorf("mail: unknown template %q", kind)
	}
	var m Message
	var err error
	if m.Subject, err = execText(t.subject, d); err != nil {
		return Message{}, err
	}
	var text, html strings.Builder
	html.WriteString(`<!doctype html><html><body style="font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:1.5;color:#222">`)
	for _, line := range t.lines {
		s, err := execText(line, d)
		if err != nil {
			return Message{}, err
		}
		text.WriteString(s + "\n\n")
		h, err := execHTML(line, d)
		if err != nil {
			return Message{}, err
		}
		html.WriteString("<p>" + h + "</p>")
	}
	html.WriteString(`<p style="color:#888;font-size:12px">` + htmltemplate.HTMLEscapeString(d.Title) + `</p></body></html>`)
	text.WriteString("-- \n" + d.Title + "\n")
	m.Text, m.HTML = text.String(), html.String()
	return m, nil
}

func execText(src string, d Data) (string, error) {
	t, err := texttemplate.New("").Funcs(textFuncs).Parse(src)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

func execHTML(src string, d Data) (string, error) {
	t, err := htmltemplate.New("").Funcs(htmlFuncs).Parse(src)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}
