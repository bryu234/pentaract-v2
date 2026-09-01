package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/bryu234/pentaract-v2/internal/httpapi"
	"github.com/bryu234/pentaract-v2/internal/storage"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"github.com/bryu234/pentaract-v2/internal/webui"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger, os.Args[1:]); err != nil {
		logger.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.EnsureDirectories(); err != nil {
		return err
	}
	ctx := context.Background()
	switch command {
	case "serve", "migrate":
		pool, err := database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := database.Migrate(ctx, pool); err != nil {
			return err
		}
		if command == "migrate" {
			logger.Info("migrations applied")
			return nil
		}
		masterKey, err := config.LoadMasterKey(cfg.MasterKeyFile)
		if err != nil {
			return err
		}
		botAPI := telegram.New(cfg.TelegramAPIURL)
		processor := &storage.Processor{DB: pool, Config: cfg, MasterKey: masterKey, Telegram: botAPI, Logger: logger}
		serverAPI := &httpapi.Server{DB: pool, Config: cfg, MasterKey: masterKey, Telegram: botAPI, Logger: logger}
		router := http.NewServeMux()
		router.Handle("/api/", serverAPI.Handler())
		router.Handle("/health/", serverAPI.Handler())
		router.Handle("/", webui.Handler())
		server := &http.Server{Addr: cfg.ListenAddress, Handler: router, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		runCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		go processor.Run(runCtx)
		go func() {
			<-runCtx.Done()
			shutdownCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
			defer done()
			_ = server.Shutdown(shutdownCtx)
		}()
		logger.Info("Pentaract V2 listening", "address", cfg.ListenAddress)
		err = server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case "backup":
		return backup(ctx, cfg, logger)
	case "restore":
		return restore(ctx, cfg, logger)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func backup(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	passphrase := os.Getenv("BACKUP_PASSPHRASE")
	if passphrase == "" {
		return errors.New("BACKUP_PASSPHRASE is required")
	}
	temp, err := os.MkdirTemp("", "pentaract-v2-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	dump := filepath.Join(temp, "database.dump")
	if err := runCommand(ctx, "pg_dump", "--dbname", cfg.DatabaseURL, "--format=custom", "--file", dump); err != nil {
		return err
	}
	key, err := os.ReadFile(cfg.MasterKeyFile)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temp, "master.key"), key, 0o600); err != nil {
		return err
	}
	manifest := map[string]any{"format": 1, "created_at": time.Now().UTC(), "public_url": cfg.PublicURL, "telegram_api_url": cfg.TelegramAPIURL, "trash_retention_hours": cfg.TrashRetention.Hours(), "telegram_chunk_size": cfg.TelegramChunkSize, "telegram_api_id": os.Getenv("TELEGRAM_API_ID"), "telegram_api_hash": os.Getenv("TELEGRAM_API_HASH")}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(temp, "manifest.json"), manifestBytes, 0o600); err != nil {
		return err
	}
	archive := filepath.Join(temp, "bundle.tar.gz")
	if err := writeArchive(archive, temp, []string{"database.dump", "master.key", "manifest.json"}); err != nil {
		return err
	}
	name := fmt.Sprintf("pentaract-v2-%s.pv2backup", time.Now().UTC().Format("20060102T150405Z"))
	output := filepath.Join(cfg.DataDir, "backups", name)
	if err := encryptBackup(archive, output, passphrase); err != nil {
		return fmt.Errorf("encrypt backup: %w", err)
	}
	logger.Info("backup created", "path", output)
	return nil
}

func restore(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	input, passphrase := os.Getenv("BACKUP_FILE"), os.Getenv("BACKUP_PASSPHRASE")
	if input == "" || passphrase == "" {
		return errors.New("BACKUP_FILE and BACKUP_PASSPHRASE are required")
	}
	if !filepath.IsAbs(input) {
		input = filepath.Join(cfg.DataDir, "backups", filepath.Base(input))
	}
	temp, err := os.MkdirTemp("", "pentaract-v2-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	archive := filepath.Join(temp, "bundle.tar.gz")
	if err := decryptBackup(input, archive, passphrase); err != nil {
		return fmt.Errorf("decrypt backup: %w", err)
	}
	if err := extractArchive(archive, temp); err != nil {
		return err
	}
	if err := runCommand(ctx, "pg_restore", "--dbname", cfg.DatabaseURL, "--clean", "--if-exists", "--no-owner", filepath.Join(temp, "database.dump")); err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(temp, "master.key"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfg.MasterKeyFile, key, 0o600); err != nil {
		return err
	}
	logger.Info("backup restored", "source", input)
	return nil
}

func encryptBackup(input, output, passphrase string) (returnErr error) {
	identity, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	identity.SetWorkFactor(18)
	source, err := os.Open(input)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := destination.Close(); returnErr == nil {
			returnErr = err
		}
		if returnErr != nil {
			_ = os.Remove(output)
		}
	}()
	encrypted, err := age.Encrypt(destination, identity)
	if err != nil {
		return err
	}
	if _, err := io.Copy(encrypted, source); err != nil {
		return err
	}
	return encrypted.Close()
}

func decryptBackup(input, output, passphrase string) (returnErr error) {
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return err
	}
	source, err := os.Open(input)
	if err != nil {
		return err
	}
	defer source.Close()
	decrypted, err := age.Decrypt(source, identity)
	if err != nil {
		return err
	}
	destination, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := destination.Close(); returnErr == nil {
			returnErr = err
		}
		if returnErr != nil {
			_ = os.Remove(output)
		}
	}()
	_, err = io.Copy(destination, decrypted)
	return err
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
func writeArchive(path, root string, names []string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return file.Close()
}
func extractArchive(path, target string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(header.Name)
		if clean == "." || strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			return errors.New("unsafe backup entry")
		}
		destination := filepath.Join(target, clean)
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported backup entry %s", clean)
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode))
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}
