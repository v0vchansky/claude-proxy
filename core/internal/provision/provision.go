// Package provision по SSH разворачивает/усыновляет AmneziaWG на VPS:
// ставит пакет при необходимости, генерит серверные ключи, настраивает awg0,
// NAT/forwarding/автозапуск и добавляет клиентский peer. Возвращает готовый профиль.
//
// SSH-доступ используется только в момент операции и нигде не сохраняется.
package provision

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHConfig — доступ к целевому серверу.
type SSHConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	Password       string `json:"password"`
	PrivateKeyPath string `json:"privateKeyPath"`
	Passphrase     string `json:"passphrase"`
}

// Params — желаемые параметры туннеля для НОВОЙ установки.
// При усыновлении уже настроенного сервера реальные значения читаются с него.
type Params struct {
	AWGPort          int    `json:"awgPort"`
	ServerVpnAddress string `json:"serverVpnAddress"`
	ClientVpnAddress string `json:"clientVpnAddress"`
	Jc               int    `json:"jc"`
	Jmin             int    `json:"jmin"`
	Jmax             int    `json:"jmax"`
	S1               int    `json:"s1"`
	S2               int    `json:"s2"`
	H1               uint32 `json:"h1"`
	H2               uint32 `json:"h2"`
	H3               uint32 `json:"h3"`
	H4               uint32 `json:"h4"`
}

// Result — то, из чего приложение соберёт ServerProfile.
type Result struct {
	ServerPublicKey  string   `json:"serverPublicKey"`
	Host             string   `json:"host"`
	Port             int      `json:"port"`
	ServerVpnAddress string   `json:"serverVpnAddress"`
	ClientVpnAddress string   `json:"clientVpnAddress"`
	Jc               int      `json:"jc"`
	Jmin             int      `json:"jmin"`
	Jmax             int      `json:"jmax"`
	S1               int      `json:"s1"`
	S2               int      `json:"s2"`
	S3               int      `json:"s3"`
	S4               int      `json:"s4"`
	H1               uint32   `json:"h1"`
	H2               uint32   `json:"h2"`
	H3               uint32   `json:"h3"`
	H4               uint32   `json:"h4"`
	Adopted          bool     `json:"adopted"`
	RebootRequired   bool     `json:"rebootRequired"`
	Log              []string `json:"log"`
}

func (p Params) withDefaults() Params {
	if p.AWGPort == 0 {
		p.AWGPort = 51820
	}
	if p.ServerVpnAddress == "" {
		p.ServerVpnAddress = "10.77.0.1"
	}
	if p.ClientVpnAddress == "" {
		p.ClientVpnAddress = "10.77.0.2"
	}
	if p.Jc == 0 && p.Jmin == 0 && p.Jmax == 0 {
		p.Jc, p.Jmin, p.Jmax = 5, 50, 1000
	}
	if p.S1 == 0 && p.S2 == 0 {
		p.S1, p.S2 = 64, 128
	}
	if p.H1 == 0 && p.H2 == 0 && p.H3 == 0 && p.H4 == 0 {
		p.H1, p.H2, p.H3, p.H4 = 1000001, 1000002, 1000003, 1000004
	}
	return p
}

// Provision выполняет установку/усыновление и возвращает профиль.
// logf (может быть nil) получает человекочитаемые шаги для UI.
func Provision(sshCfg SSHConfig, params Params, clientPublicKey string, logf func(string)) (Result, error) {
	if logf == nil {
		logf = func(string) {}
	}
	if clientPublicKey == "" {
		return Result{}, fmt.Errorf("пустой публичный ключ клиента")
	}
	params = params.withDefaults()

	client, err := dial(sshCfg)
	if err != nil {
		return Result{}, err
	}
	defer client.Close()
	logf("SSH подключение установлено")

	script := buildScript(params, clientPublicKey)
	logf("Запуск настройки на сервере…")
	// Шаги (LOG:) эмитятся ВЖИВУЮ по мере выполнения скрипта через logf — чтобы UI
	// показывал прогресс долгой установки, а не замирал до конца.
	out, runErr := run(client, script, logf)
	if runErr != nil {
		return Result{}, fmt.Errorf("%v\n%s", runErr, tail(out, 15))
	}
	if !strings.Contains(out, "PROVISION_OK") {
		return Result{}, fmt.Errorf("настройка не завершилась успешно:\n%s", tail(out, 20))
	}

	res, perr := parseResult(out)
	if perr != nil {
		return Result{}, perr
	}
	res.Host = sshCfg.Host
	res.ClientVpnAddress = stripMask(params.ClientVpnAddress)
	res.Adopted = strings.Contains(out, "MODE=adopt")
	if res.Adopted {
		logf("Сервер уже настроен — добавлен peer, параметры прочитаны с сервера")
	} else {
		logf("AmneziaWG установлен и настроен с нуля")
	}
	if res.RebootRequired {
		logf("Сервер перезагружается для активации модуля — подключение будет готово через ~1 минуту")
	}
	logf("Готово")
	return res, nil
}

func dial(cfg SSHConfig) (*ssh.Client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("host пуст")
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	user := cfg.User
	if user == "" {
		user = "root"
	}

	var auth []ssh.AuthMethod
	if cfg.PrivateKeyPath != "" {
		key, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("чтение приватного ключа: %w", err)
		}
		var signer ssh.Signer
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("разбор приватного ключа: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auth = append(auth, ssh.Password(cfg.Password))
		auth = append(auth, ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			ans := make([]string, len(questions))
			for i := range ans {
				ans[i] = cfg.Password
			}
			return ans, nil
		}))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("не задан ни пароль, ни приватный ключ")
	}

	clientCfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // сервер указывает сам владелец
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	client, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("SSH подключение не удалось: %w", err)
	}
	return client, nil
}

// run выполняет скрипт на сервере, стримя вывод построчно. Строки вида "LOG:…"
// из stdout передаются в onLog ВЖИВУЮ (для прогресса в UI). Весь вывод (stdout+stderr)
// также накапливается и возвращается для финального разбора маркеров результата.
func run(client *ssh.Client, script string, onLog func(string)) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("SSH сессия: %w", err)
	}
	defer sess.Close()

	stdoutPipe, err := sess.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := sess.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	var buf bytes.Buffer
	var bufMu sync.Mutex

	// Keepalive на время выполнения: установка пакета/сборка DKMS идут долго и молча,
	// а канал без трафика рвётся idle-таймаутом NAT/sshd.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_, _, _ = client.SendRequest("keepalive@openssh.com", true, nil)
			}
		}
	}()

	// Передаём скрипт как base64 одной командой: надёжнее, чем piped stdin в `bash -s`.
	enc := base64.StdEncoding.EncodeToString([]byte(script))
	cmd := "echo " + enc + " | base64 --decode | bash"
	if err := sess.Start(cmd); err != nil {
		close(stop)
		return "", fmt.Errorf("запуск команды: %w", err)
	}

	scan := func(r io.Reader, live bool, wg *sync.WaitGroup) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			bufMu.Lock()
			buf.WriteString(line)
			buf.WriteByte('\n')
			bufMu.Unlock()
			if live && onLog != nil {
				if s, ok := strings.CutPrefix(strings.TrimSpace(line), "LOG:"); ok {
					onLog(strings.TrimSpace(s))
				}
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go scan(stdoutPipe, true, &wg)
	go scan(stderrPipe, false, &wg)

	waitErr := sess.Wait()
	wg.Wait()
	close(stop)

	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()
	return out, waitErr
}

func parseResult(out string) (Result, error) {
	var r Result
	get := func(key string) string {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(line, key+"="); ok {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	r.ServerPublicKey = get("SERVER_PUBLIC_KEY")
	if r.ServerPublicKey == "" {
		return Result{}, fmt.Errorf("не удалось получить публичный ключ сервера")
	}
	r.RebootRequired = get("NEEDS_REBOOT") == "1"
	r.ServerVpnAddress = stripMask(get("SERVER_VPN"))
	r.Port = atoiDef(get("AWG_PORT"), 51820)
	r.Jc = atoiDef(get("JC"), 0)
	r.Jmin = atoiDef(get("JMIN"), 0)
	r.Jmax = atoiDef(get("JMAX"), 0)
	r.S1 = atoiDef(get("S1"), 0)
	r.S2 = atoiDef(get("S2"), 0)
	r.S3 = atoiDef(get("S3"), 0)
	r.S4 = atoiDef(get("S4"), 0)
	r.H1 = uint32(atoiDef(get("H1"), 0))
	r.H2 = uint32(atoiDef(get("H2"), 0))
	r.H3 = uint32(atoiDef(get("H3"), 0))
	r.H4 = uint32(atoiDef(get("H4"), 0))
	return r, nil
}

func atoiDef(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func stripMask(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
