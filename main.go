package main

import (
	"context"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guregu/intertube/email"
	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
	"github.com/guregu/intertube/web"
)

var (
	domainFlag = flag.String("domain", "", "domain")
	bindFlag   = flag.String("addr", ":8000", "addr to bind on")
	cfgFlag    = flag.String("cfg", "config.toml", "configuration file location")
	assetsFlag = flag.String("assets", "", "assets directory (default: next to the executable)")
	debugFlag  = flag.Bool("debug", false, "development mode: insecure cookies, template reloading, verbose Subsonic logs")
)

func main() {
	flag.Parse()

	cfg, err := readConfig(*cfgFlag)
	if err != nil {
		log.Fatalln("Failed to read config file:", *cfgFlag, "error:", err)
	}
	web.Domain = cfg.Domain
	if *domainFlag != "" {
		web.Domain = *domainFlag
	}

	web.AdminIDs = cfg.AdminIDs
	web.SiteText = cfg.siteText()
	web.DebugMode = *debugFlag
	web.AssetsDir = *assetsFlag

	prices, err := cfg.pricing()
	if err != nil {
		log.Fatalln("Invalid billing config:", err)
	}
	tube.Prices = prices
	web.ManualPayment = strings.TrimSpace(cfg.Billing.ManualPayment)

	err = web.SetBTCPay(web.BTCPayConfig{
		URL:           cfg.BTCPay.URL,
		StoreID:       cfg.BTCPay.StoreID,
		APIKey:        cfg.BTCPay.APIKey,
		WebhookSecret: cfg.BTCPay.WebhookSecret,
	})
	if err != nil {
		log.Fatalln("Invalid btcpay config:", err)
	}
	if err := web.SetMail(email.Config{
		Host:     cfg.Mail.Host,
		Port:     cfg.Mail.Port,
		Username: cfg.Mail.Username,
		Password: cfg.Mail.Password,
		From:     cfg.Mail.From,
		TLS:      cfg.Mail.TLS,
	}); err != nil {
		log.Fatalln("Invalid mail config:", err)
	}
	tube.RequireVerifiedEmail = email.IsEnabled()
	web.RealIPHeader = cfg.RealIPHeader

	key, err := hex.DecodeString(cfg.SecretKey)
	if err != nil || len(key) != 32 {
		log.Fatalln("secret_key must be 64 hex characters. Generate one with: openssl rand -hex 32")
	}
	if err := tube.SetSecretKey(key); err != nil {
		log.Fatalln("Invalid secret_key:", err)
	}

	ctx := context.Background()
	if err := tube.Init(ctx, cfg.DB.Path); err != nil {
		log.Fatalln("Failed to open database:", cfg.DB.Path, "error:", err)
	}
	defer tube.Close()

	err = storage.Init(storage.Config{
		Type:            storage.StorageType(cfg.Storage.Type),
		FilesBucket:     cfg.Storage.FilesBucket,
		AccessKeyID:     cfg.Storage.AccessKeyID,
		AccessKeySecret: cfg.Storage.AccessKeySecret,
		Region:          cfg.Storage.Region,
		Endpoint:        cfg.Storage.Endpoint,
		CFAccountID:     cfg.Storage.CloudflareAccount,
	})
	if err != nil {
		log.Fatalln("Failed to set up storage:", err)
	}
	if r := cfg.Replica; r.Bucket != "" {
		lockDays := 7
		if r.LockDays != nil {
			lockDays = *r.LockDays
		}
		if lockDays < 0 || r.DelayDays < 0 {
			log.Fatalln("replica.lock_days and replica.delay_days can't be negative")
		}
		err := storage.InitReplica(storage.Config{
			Type:            storage.StorageType(r.Type),
			FilesBucket:     r.Bucket,
			AccessKeyID:     r.AccessKeyID,
			AccessKeySecret: r.AccessKeySecret,
			Region:          r.Region,
			Endpoint:        r.Endpoint,
			CFAccountID:     r.CloudflareAccount,
		}, storage.ReplicaOptions{LockDays: lockDays, StorageClass: r.StorageClass, DelayDays: r.DelayDays})
		if err != nil {
			log.Fatalln("Failed to set up replica storage:", err)
		}
	}
	if cfg.Cache.Dir != "" && cfg.Cache.MaxGB > 0 {
		cache, err := storage.NewBlockCache(cfg.Cache.Dir, int64(cfg.Cache.MaxGB*1e9))
		if err != nil {
			log.Fatalln("Failed to set up cache:", err)
		}
		storage.Cache = cache
	}
	web.SetUploadConcurrency(cfg.Uploads.Concurrency)
	web.TempDir = filepath.Join(filepath.Dir(cfg.DB.Path), "tmp")
	if err := os.MkdirAll(web.TempDir, 0o700); err != nil {
		log.Fatalln("Failed to create temp dir:", err)
	}

	log.Println("Build date:", web.Deployed)
	web.Load()
	web.StartBilling(ctx)
	web.StartReplication(ctx)

	if web.DebugMode {
		closeWatch := web.WatchFiles()
		defer closeWatch()
	}

	srv := &http.Server{
		Addr:              *bindFlag,
		Handler:           web.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Println("Listening on", *bindFlag)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalln(err)
	}
}
