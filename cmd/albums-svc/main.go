// Команда albums-svc — сервис онлайн-листалок.
//
//	albums-svc -config <файл>             запуск сервера
//	albums-svc health -config <файл>      проверка живости по сокету (HEALTHCHECK юнита)
//	albums-svc verify -data <каталог>     проверка копии data/ только на чтение
//	albums-svc after-restore -data <кат>  после восстановления из копии, при остановленном сервисе
//	albums-svc -clock-ok -data <каталог>  снять паузу сторожа часов, при остановленном сервисе
//	albums-svc -gen-secret | -gen-token   разовые выдачи для машины владельца
//
// У подкоманд свои FlagSet: пакет flag останавливается на первом позиционном аргументе. Секреты печатаются в
// stdout и не запускаются из юнита (иначе вывод ушёл бы в journald): команда запускается на машине владельца,
// значение переносится в менеджер паролей и в secret Podman.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/config"
)

// Коды выхода.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	// SIGTERM — так Podman останавливает контейнер; SIGINT — Ctrl+C на машине разработчика.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	code := execute(ctx, os.Args[1:], os.Stdout, os.Stderr, rand.Reader)
	stop()
	os.Exit(code)
}

// run — команда без сигналов: для разовых команд и тестов.
func run(args []string, stdout, stderr io.Writer, rnd io.Reader) int {
	return execute(context.Background(), args, stdout, stderr, rnd)
}

// execute — вся команда; rnd — источник случайных байт (в работе crypto/rand), чтобы тест мог подставить отказ;
// отмена ctx останавливает сервер.
func execute(ctx context.Context, args []string, stdout, stderr io.Writer, rnd io.Reader) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "health":
			return cmdHealth(ctx, args[1:], stdout, stderr)
		case "verify":
			return cmdVerify(args[1:], stdout, stderr)
		case "after-restore":
			return cmdAfterRestore(args[1:], stdout, stderr)
		}
		_, _ = fmt.Fprintf(stderr, "albums-svc: неизвестная подкоманда %q (health, verify, after-restore)\n", args[0])
		return exitUsage
	}
	fs := flag.NewFlagSet("albums-svc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "запустить сервер с этим конфигом")
	genSecret := fs.Bool("gen-secret", false, "напечатать новый мастер-секрет (base64url, 32 байта) и выйти")
	genToken := fs.Bool("gen-token", false, "напечатать новый токен загрузки и строку sha256 для конфига и выйти")
	clockOK := fs.Bool("clock-ok", false, "снять паузу сторожа часов (нужен -data; сервис остановлен) и выйти")
	dataDir := fs.String("data", "", "каталог data/ для -clock-ok")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "albums-svc: лишние аргументы")
		return exitUsage
	}
	modes := 0
	for _, on := range []bool{*genSecret, *genToken, *clockOK, *cfgPath != ""} {
		if on {
			modes++
		}
	}
	switch {
	case modes > 1:
		_, _ = fmt.Fprintln(stderr, "albums-svc: -gen-secret, -gen-token, -clock-ok и -config — по одному")
		return exitUsage
	case *genSecret:
		return printSecret(stdout, stderr, rnd)
	case *genToken:
		return printToken(stdout, stderr, rnd)
	case *clockOK:
		return cmdClockOK(*dataDir, stdout, stderr)
	case *cfgPath != "":
		return serve(ctx, *cfgPath, stdout, stderr)
	}
	_, _ = fmt.Fprintln(stderr, "albums-svc: нужна команда: -config <файл>, health, verify, after-restore, -clock-ok, -gen-secret или -gen-token")
	return exitUsage
}

func printSecret(stdout, stderr io.Writer, rnd io.Reader) int {
	s, err := config.GenSecret(rnd)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc: секрет не выдан:", err)
		return exitFail
	}
	if _, err := fmt.Fprintln(stdout, s); err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc: вывод не удался:", err)
		return exitFail
	}
	return exitOK
}

// printToken печатает токен (его вводят в программе) и sha256 (его вписывают в конфиг, а сам токен там не
// хранится). Одна строка — одно значение, с подписью: перепутать, что куда, нельзя.
func printToken(stdout, stderr io.Writer, rnd io.Reader) int {
	tok, sum, err := access.NewToken(rnd)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc: токен не выдан:", err)
		return exitFail
	}
	if _, err := fmt.Fprintf(stdout, "token:  %s\nsha256: %s\n", tok, sum); err != nil {
		_, _ = fmt.Fprintln(stderr, "albums-svc: вывод не удался:", err)
		return exitFail
	}
	return exitOK
}
