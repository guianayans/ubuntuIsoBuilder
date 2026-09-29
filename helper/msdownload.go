package main

// Download da ISO oficial do Windows 11 direto da Microsoft, no PC do cliente,
// pelo mesmo fluxo público do site https://www.microsoft.com/software-download/windows11
// (o que o script Fido do Rufus também faz):
//   1. página do produto: ID da edição ("multi-edition ISO") e a tabela de SHA256 por idioma;
//   2. sessão: um GUID novo, registrado no "ov-df" (mdt.js) e no vlscppe;
//   3. lista de idiomas (SKUs) da edição;
//   4. link temporário (24 h) do SKU escolhido, com Referer da página.
// O painel nunca hospeda a ISO (licença da Microsoft).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	msUA         = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36 Edg/138.0.0.0"
	msProfile    = "606624d44113"
	msInstanceID = "560dc9f3-1aa5-4a2f-b63c-9e18f8d0e175"
)

// msBase: pode ser trocado nos testes (UAB_MS_BASE; aí também não fala com ov-df/vlscppe).
var msBase = func() string {
	if v := os.Getenv("UAB_MS_BASE"); strings.HasPrefix(v, "http://127.0.0.1:") {
		return v
	}
	return "https://www.microsoft.com"
}()

func msReal() bool { return msBase == "https://www.microsoft.com" }

// Idioma (código do painel) -> nome do idioma no site da Microsoft ("Language" do SKU).
var msLangNames = map[string]string{
	"ar-sa": "Arabic", "pt-br": "Brazilian Portuguese", "bg-bg": "Bulgarian", "zh-cn": "Chinese (Simplified)",
	"zh-tw": "Chinese (Traditional)", "hr-hr": "Croatian", "cs-cz": "Czech", "da-dk": "Danish", "nl-nl": "Dutch",
	"en-us": "English", "en-gb": "English International", "et-ee": "Estonian", "fi-fi": "Finnish", "fr-fr": "French",
	"fr-ca": "French Canadian", "de-de": "German", "el-gr": "Greek", "he-il": "Hebrew", "hu-hu": "Hungarian",
	"it-it": "Italian", "ja-jp": "Japanese", "ko-kr": "Korean", "lv-lv": "Latvian", "lt-lt": "Lithuanian",
	"nb-no": "Norwegian", "pl-pl": "Polish", "pt-pt": "Portuguese", "ro-ro": "Romanian", "ru-ru": "Russian",
	"sr-latn-rs": "Serbian Latin", "sk-sk": "Slovak", "sl-si": "Slovenian", "es-es": "Spanish", "es-mx": "Spanish (Mexico)",
	"sv-se": "Swedish", "th-th": "Thai", "tr-tr": "Turkish", "uk-ua": "Ukrainian",
}

// errMSBlocked: a Microsoft recusou o download automático (acontece com VPN, proxy, alguns provedores).
var errMSBlocked = errors.New("a Microsoft recusou o download automático a partir desta conexão (isso acontece com VPN, proxy ou alguns provedores)")

type msLink struct {
	URL    string
	Name   string
	Size   int64
	SHA256 string // da tabela da página ("" se não achou)
}

type msClient struct {
	c   *http.Client
	ctx context.Context
}

func newMSClient(ctx context.Context) *msClient {
	jar, _ := cookiejar.New(nil)
	return &msClient{c: &http.Client{Jar: jar, Timeout: 60 * time.Second}, ctx: ctx}
}

func (m *msClient) get(u, referer string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", msUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := m.c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, resp.StatusCode, err
}

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0F | 0x40
	b[8] = b[8]&0x3F | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var (
	reMSOption = regexp.MustCompile(`<option value="(\d+)">([^<]*)</option>`)
	reMSHash   = regexp.MustCompile(`(?i)<td>([^<]{2,80})</td>\s*<td>([0-9a-f]{64})</td>`)
	reMdtURL   = regexp.MustCompile(`url:"(https://ov-df\.microsoft\.com/[^"]+)"`)
	reMdtTicks = regexp.MustCompile(`rticks="\+(\d+)`)
	reISOName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,120}\.iso$`)
)

func normLang(s string) string {
	s = strings.ToLower(html.UnescapeString(s))
	for _, x := range []string{"64-bit", "arm64", "(", ")"} {
		s = strings.ReplaceAll(s, x, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

// parseMSPage devolve o ID da edição (ISO multi-edição da arquitetura) e a tabela idioma->SHA256.
func parseMSPage(page, arch string) (string, map[string]string) {
	id := ""
	for _, m := range reMSOption.FindAllStringSubmatch(page, -1) {
		label := strings.ToLower(m[2])
		if !strings.Contains(label, "windows 11") {
			continue
		}
		if (arch == "arm64") == strings.Contains(label, "arm64") {
			id = m[1]
			break
		}
		if id == "" {
			id = m[1]
		}
	}
	hashes := map[string]string{}
	for _, m := range reMSHash.FindAllStringSubmatch(page, -1) {
		hashes[normLang(m[1])] = strings.ToLower(m[2])
	}
	return id, hashes
}

type msSKU struct {
	ID                string `json:"Id"`
	Language          string `json:"Language"`
	LocalizedLanguage string `json:"LocalizedLanguage"`
}

type msErrs struct {
	Errors []struct {
		Key, Value string
		Type       int
	} `json:"Errors"`
	ValidationContainer struct {
		Errors []struct {
			Key, Value string
			Type       int
		}
	}
}

func (e msErrs) err() error {
	all := append(e.Errors, e.ValidationContainer.Errors...)
	for _, x := range all {
		if x.Type == 9 || strings.Contains(x.Key, "Sentinel") || strings.Contains(x.Value, "715-123130") {
			return errMSBlocked
		}
	}
	if len(all) > 0 {
		return fmt.Errorf("o site da Microsoft respondeu: %s", all[0].Value)
	}
	return nil
}

// msGetLink obtém o link temporário da ISO do Windows 11 no idioma/arquitetura pedidos.
func msGetLink(ctx context.Context, lang, arch string) (*msLink, error) {
	m := newMSClient(ctx)
	langName := msLangNames[strings.ToLower(lang)]
	if langName == "" {
		return nil, fmt.Errorf("idioma %q não disponível para download automático", lang)
	}
	pageName := "windows11"
	if arch == "arm64" {
		pageName = "windows11arm64"
	}
	pageURL := msBase + "/en-us/software-download/" + pageName
	page, code, err := m.get(pageURL, "", 4<<20)
	if err != nil {
		return nil, errors.New("sem acesso ao site da Microsoft (confira a internet)")
	}
	if code != 200 {
		return nil, fmt.Errorf("o site da Microsoft respondeu %d", code)
	}
	edition, hashes := parseMSPage(string(page), arch)
	if edition == "" {
		return nil, errors.New("não achei a ISO do Windows 11 na página da Microsoft (o site mudou?)")
	}
	session := uuid4()
	// registro da sessão (o site faz isso pelo navegador; sem isso o link é recusado)
	if !msReal() {
		// testes: servidor falso, sem os registros externos
	} else if mdt, _, err := m.get("https://ov-df.microsoft.com/mdt.js?instanceId="+msInstanceID+"&pageId=si&session_id="+session, pageURL, 1<<20); err == nil {
		if u := reMdtURL.FindSubmatch(mdt); u != nil {
			ticks := "0"
			if t := reMdtTicks.FindSubmatch(mdt); t != nil {
				ticks = string(t[1])
			}
			_, _, _ = m.get(string(u[1])+"&mdt="+strconv.FormatInt(time.Now().UnixMilli(), 10)+"&rticks="+ticks, pageURL, 1<<20)
		}
	}
	if msReal() {
		_, _, _ = m.get("https://vlscppe.microsoft.com/tags?org_id=y6jn8c31&session_id="+session, pageURL, 1<<20)
	}

	api := msBase + "/software-download-connector/api/"
	q := url.Values{"profile": {msProfile}, "ProductEditionId": {edition}, "SKU": {"undefined"},
		"friendlyFileName": {"undefined"}, "Locale": {"en-US"}, "sessionID": {session}}
	b, code, err := m.get(api+"getskuinformationbyproductedition?"+q.Encode(), pageURL, 1<<20)
	if err != nil {
		return nil, errors.New("sem acesso ao site da Microsoft (confira a internet)")
	}
	var skus struct {
		msErrs
		Skus []msSKU `json:"Skus"`
	}
	if err := json.Unmarshal(b, &skus); err != nil {
		return nil, fmt.Errorf("resposta inesperada do site da Microsoft (%d)", code)
	}
	if err := skus.err(); err != nil {
		return nil, err
	}
	var sku *msSKU
	for i := range skus.Skus {
		s := &skus.Skus[i]
		if normLang(s.Language) == normLang(langName) || normLang(s.LocalizedLanguage) == normLang(langName) {
			sku = s
			break
		}
	}
	if sku == nil {
		return nil, fmt.Errorf("a Microsoft não oferece o Windows 11 em %s (%s)", lang, langName)
	}
	q = url.Values{"profile": {msProfile}, "productEditionId": {"undefined"}, "SKU": {sku.ID},
		"friendlyFileName": {"undefined"}, "Locale": {"en-US"}, "sessionID": {session}}
	b, code, err = m.get(api+"GetProductDownloadLinksBySku?"+q.Encode(), msBase+"/software-download/"+pageName, 1<<20)
	if err != nil {
		return nil, errors.New("sem acesso ao site da Microsoft (confira a internet)")
	}
	var links struct {
		msErrs
		Options []struct {
			Uri          string
			DownloadType int
		} `json:"ProductDownloadOptions"`
	}
	if err := json.Unmarshal(b, &links); err != nil {
		return nil, fmt.Errorf("resposta inesperada do site da Microsoft (%d)", code)
	}
	if err := links.err(); err != nil {
		return nil, err
	}
	want := map[string]int{"x64": 1, "arm64": 2}[arch]
	link := ""
	for _, o := range links.Options {
		if o.DownloadType == want || link == "" {
			link = o.Uri
		}
	}
	if link == "" {
		return nil, errMSBlocked
	}
	pu, err := url.Parse(link)
	if err != nil || (pu.Scheme != "https" && msReal()) || !(strings.HasSuffix(pu.Hostname(), ".microsoft.com") || !msReal()) {
		return nil, errors.New("link de download inesperado")
	}
	name := path.Base(pu.Path)
	if !reISOName.MatchString(name) {
		name = "Windows11_" + strings.ReplaceAll(langName, " ", "") + "_" + arch + ".iso"
	}
	out := &msLink{URL: link, Name: name, SHA256: hashes[normLang(sku.Language+" 64-bit")]}
	if out.SHA256 == "" {
		out.SHA256 = hashes[normLang(sku.LocalizedLanguage)]
	}
	// tamanho (pedido de 1 byte: também confirma que o link funciona)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	req.Header.Set("User-Agent", msUA)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, errors.New("sem acesso ao servidor de download da Microsoft (confira a internet)")
	}
	resp.Body.Close()
	if cr := resp.Header.Get("Content-Range"); resp.StatusCode == http.StatusPartialContent && strings.Contains(cr, "/") {
		out.Size, _ = strconv.ParseInt(cr[strings.LastIndex(cr, "/")+1:], 10, 64)
	} else if resp.StatusCode == 200 {
		out.Size = resp.ContentLength
	} else {
		return nil, fmt.Errorf("o servidor de download da Microsoft respondeu %s", resp.Status)
	}
	if out.Size < 1<<30 {
		return nil, errors.New("tamanho inesperado da ISO no servidor da Microsoft")
	}
	return out, nil
}

// downloadWindowsISO baixa (com retomada) e confere a ISO. Devolve caminho e SHA256.
func downloadWindowsISO(ctx context.Context, w *BundleWindows, p *progress) (string, string, error) {
	p.set("connecting", "Pedindo à Microsoft o link do Windows 11…", 0)
	var link *msLink
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		link, err = msGetLink(ctx, w.Lang, w.Arch)
		if err == nil || errors.Is(err, errMSBlocked) || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt) * 3 * time.Second)
	}
	if err != nil {
		return "", "", err
	}
	dir := downloadDir()
	dest := filepath.Join(dir, link.Name)
	if fi, err := os.Stat(dest); err == nil && fi.Size() == link.Size {
		if sum, err := hashFile(dest, p); err == nil && (link.SHA256 == "" || sum == link.SHA256) {
			if _, err := inspectWindowsISO(dest, w.Arch, w.Edition, w.Lang); err == nil {
				return dest, sum, nil
			}
		}
	}
	// srcTag identifica QUAL arquivo o .part contém: a Microsoft às vezes troca o conteúdo
	// mantendo o nome; retomar em cima de outra versão mistura as duas e o SHA256 não bate.
	srcTag := func(l *msLink) string { return fmt.Sprintf("%s %d %s", l.SHA256, l.Size, l.Name) }

	fetch := func() (string, error) {
		part := dest + ".part"
		if b, err := os.ReadFile(part + ".src"); err != nil || strings.TrimSpace(string(b)) != srcTag(link) {
			os.Remove(part) // .part de outra versão (ou sem origem conhecida): recomeça do zero
		}
		client := &http.Client{Timeout: 0}
		label := fmt.Sprintf("Baixando o Windows 11 da Microsoft (%s)…", link.Name)
		var lastErr error
		for attempt := 1; attempt <= 40; attempt++ {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			have := int64(0)
			if fi, err := os.Stat(part); err == nil {
				have = fi.Size()
			}
			if have >= link.Size {
				break
			}
			p.set("downloading", label, link.Size)
			p.add(have)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
			req.Header.Set("User-Agent", msUA)
			if have > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
			}
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				p.setMessage(fmt.Sprintf("Sem conexão, tentando de novo (%d)…", attempt))
				sleepCtx(ctx, time.Duration(min(attempt, 10))*3*time.Second)
				continue
			}
			switch {
			case resp.StatusCode == http.StatusOK:
				have = 0
			case resp.StatusCode == http.StatusPartialContent && have > 0:
			case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
				resp.Body.Close() // link expirou (vale 24 h): pede outro
				p.setMessage("Renovando o link de download…")
				nl, err := msGetLink(ctx, w.Lang, w.Arch)
				if err != nil {
					return "", err
				}
				if srcTag(nl) != srcTag(link) { // saiu versão nova: recomeça
					os.Remove(part)
					os.Remove(part + ".src")
					dest = filepath.Join(dir, nl.Name)
					part = dest + ".part"
				}
				link = nl
				continue
			default:
				resp.Body.Close()
				lastErr = fmt.Errorf("servidor da Microsoft respondeu %s", resp.Status)
				sleepCtx(ctx, 5*time.Second)
				continue
			}
			flags := os.O_CREATE | os.O_WRONLY
			if have > 0 {
				flags |= os.O_APPEND
			} else {
				flags |= os.O_TRUNC
				p.set("downloading", label, link.Size)
				if err := os.WriteFile(part+".src", []byte(srcTag(link)+"\n"), 0o644); err != nil {
					resp.Body.Close()
					return "", fmt.Errorf("não foi possível salvar em %s: %w", dir, err)
				}
			}
			f, err := os.OpenFile(part, flags, 0o644)
			if err != nil {
				resp.Body.Close()
				return "", fmt.Errorf("não foi possível salvar em %s: %w", dir, err)
			}
			_, cerr := io.CopyBuffer(counter{f, p}, io.LimitReader(resp.Body, link.Size-have), make([]byte, 1<<20))
			resp.Body.Close()
			f.Close()
			if cerr == nil {
				if fi, err := os.Stat(part); err == nil && fi.Size() >= link.Size {
					break
				}
			}
			lastErr = cerr
			p.setMessage("Conexão interrompida; retomando…")
			sleepCtx(ctx, 3*time.Second)
		}
		if fi, err := os.Stat(part); err != nil || fi.Size() != link.Size {
			if lastErr == nil {
				lastErr = errors.New("download incompleto")
			}
			return "", fmt.Errorf("não foi possível baixar o Windows: %v", lastErr)
		}
		return part, nil
	}

	for round := 1; ; round++ {
		part, err := fetch()
		if err != nil {
			return "", "", err
		}
		sum, err := hashFile(part, p)
		if err != nil {
			return "", "", err
		}
		if link.SHA256 != "" && sum != link.SHA256 {
			os.Remove(part)
			os.Remove(part + ".src")
			if round == 1 { // 1ª vez: baixa de novo do zero, com link novo
				p.set("connecting", "A ISO não bateu com o SHA256 publicado pela Microsoft; baixando de novo do zero…", 0)
				if nl, err := msGetLink(ctx, w.Lang, w.Arch); err == nil {
					link = nl
					dest = filepath.Join(dir, nl.Name)
				}
				continue
			}
			return "", "", errors.New("a ISO baixada veio corrompida duas vezes (o SHA256 não confere com o publicado pela Microsoft). Baixe a ISO no site da Microsoft e use \"Já tenho a ISO\"")
		}
		p.set("checking", "Conferindo o conteúdo da ISO…", 0)
		if _, err := inspectWindowsISO(part, w.Arch, w.Edition, w.Lang); err != nil {
			os.Remove(part)
			os.Remove(part + ".src")
			return "", "", fmt.Errorf("a ISO baixada não passou na conferência: %v", err)
		}
		os.Remove(part + ".src")
		if err := os.Rename(part, dest); err != nil {
			return "", "", err
		}
		if link.SHA256 != "" {
			_ = os.WriteFile(dest+".sha256", []byte(sum+"  "+link.Name+"\n"), 0o644)
		}
		return dest, sum, nil
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
