package netcfg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// commandTimeout — таймаут на одну системную команду. Команды read-only и
// быстрые; потолок защищает от зависшей утилиты, не ломая обычный запуск.
const commandTimeout = 8 * time.Second

// runner выполняет системную команду и возвращает её stdout.
// Вынесен в переменную, чтобы тесты могли подменить exec без доступа к ОС.
var runner = runCommand

// runCommand запускает read-only команду с таймаутом и возвращает stdout.
// Отсутствие бинаря и ненулевой код возврата превращаются в понятную ошибку.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	out, err := exec.CommandContext(cctx, name, args...).Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("netcfg: команда %q не найдена в PATH: %w", name, err)
		}
		if cctx.Err() != nil {
			return "", fmt.Errorf("netcfg: команда %q не уложилась в таймаут %s: %w", name, commandTimeout, cctx.Err())
		}
		// exec.ExitError несёт stderr — приложим его к сообщению.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("netcfg: команда %q завершилась с ошибкой: %w: %s", name, err, string(ee.Stderr))
		}
		return "", fmt.Errorf("netcfg: команда %q: %w", name, err)
	}
	return string(out), nil
}

// PhysicalInterface возвращает имя физического default-интерфейса (en0/en1/…),
// игнорируя utun стороннего/системного VPN (строки default с link#-шлюзом).
//
// Это лёгкий срез Capture: дёргает только `netstat -rn -f inet` и парсит его тем
// же parseDefaultRoute (метод wg-quick darwin, §2.4). Прокси-ядро зовёт его при
// connect, чтобы привязать WG-транспорт к физическому интерфейсу через IP_BOUND_IF
// (tunnel.Open boundIf) — ДО и независимо от Полного VPN. Отдельная функция, а не
// полный Capture, намеренно: не тащим per-service DNS-опрос в путь подключения
// прокси (он не нужен и мог бы упасть на капризном сервисе).
func PhysicalInterface(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	routeOut, err := runner(ctx, "netstat", "-rn", "-f", "inet")
	if err != nil {
		return "", err
	}
	_, device, err := parseDefaultRoute(routeOut)
	if err != nil {
		return "", err
	}
	return device, nil
}

// Capture снимает read-only снимок сетевой конфигурации macOS.
//
// Дёргает (ничего не меняя):
//   - netstat -rn -f inet                      → физический default-маршрут;
//   - networksetup -listnetworkserviceorder    → карта сервис → устройство;
//   - networksetup -getdnsservers <service>    → DNS каждого включённого сервиса.
//
// Функция названа Capture (а не Snapshot), потому что Snapshot — имя типа
// результата: в Go тип и функция в одном пакете не могут делить имя.
func Capture(ctx context.Context) (*Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	routeOut, err := runner(ctx, "netstat", "-rn", "-f", "inet")
	if err != nil {
		return nil, err
	}
	gateway, device, err := parseDefaultRoute(routeOut)
	if err != nil {
		return nil, err
	}

	orderOut, err := runner(ctx, "networksetup", "-listnetworkserviceorder")
	if err != nil {
		return nil, err
	}
	services := parseServiceOrder(orderOut)

	dns := make(map[string][]string)
	for _, svc := range services {
		// DNS снимаем только с включённых сервисов: отключённые трафик не несут.
		if !svc.Enabled {
			continue
		}
		dnsOut, err := runner(ctx, "networksetup", "-getdnsservers", svc.Name)
		if err != nil {
			return nil, err
		}
		dns[svc.Name] = parseDNS(dnsOut)
	}

	return &Snapshot{
		DefaultRoute: DefaultRoute{Gateway: gateway, Device: device},
		Services:     services,
		DNS:          dns,
	}, nil
}
