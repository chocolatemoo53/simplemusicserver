package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/pelletier/go-toml/v2"

	"github.com/guregu/intertube/tube"
)

type Config struct {
	Domain       string `toml:"domain"`
	AdminIDs     []int  `toml:"admin_ids"`
	SecretKey    string `toml:"secret_key"`
	RealIPHeader string `toml:"real_ip_header"`
	Uploads      struct {
		Concurrency int `toml:"concurrency"`
	} `toml:"uploads"`
	Cache struct {
		Dir   string  `toml:"dir"`
		MaxGB float64 `toml:"max_gb"`
	} `toml:"cache"`
	DB struct {
		Path string `toml:"path"`
	} `toml:"db"`
	Storage struct {
		Type              string `toml:"type"`
		FilesBucket       string `toml:"files_bucket"`
		AccessKeyID       string `toml:"access_key_id"`
		AccessKeySecret   string `toml:"access_key_secret"`
		CloudflareAccount string `toml:"cloudflare_account"`
		Region            string `toml:"region"`
		Endpoint          string `toml:"endpoint"`
	} `toml:"storage"`
	Replica struct {
		Type              string `toml:"type"`
		Bucket            string `toml:"bucket"`
		AccessKeyID       string `toml:"access_key_id"`
		AccessKeySecret   string `toml:"access_key_secret"`
		CloudflareAccount string `toml:"cloudflare_account"`
		Region            string `toml:"region"`
		Endpoint          string `toml:"endpoint"`
		LockDays          *int   `toml:"lock_days"`
		StorageClass      string `toml:"storage_class"`
		DelayDays         int    `toml:"delay_days"`
	} `toml:"replica"`
	Billing struct {
		FreeGB        int64   `toml:"free_gb"`
		PerGBMonthUSD float64 `toml:"per_gb_month_usd"`
		GraceDays     int     `toml:"grace_days"`
		LockDays      int     `toml:"lock_days"`
		RefundDays    int     `toml:"refund_days"`
		RefundFeePct  float64 `toml:"refund_fee_percent"`
		ManualPayment string  `toml:"manual_payment"`
	} `toml:"billing"`
	BTCPay struct {
		URL           string `toml:"url"`
		StoreID       string `toml:"store_id"`
		APIKey        string `toml:"api_key"`
		WebhookSecret string `toml:"webhook_secret"`
	} `toml:"btcpay"`
	Site struct {
		Name            string `toml:"name"`
		Operator        string `toml:"operator"`
		Contact         string `toml:"contact"`
		SecurityContact string `toml:"security_contact"`
		DMCAAgent       string `toml:"dmca_agent"`
		GoverningLaw    string `toml:"governing_law"`
		PaymentMethods  string `toml:"payment_methods"`
		Providers       struct {
			Hosting string `toml:"hosting"`
			Storage string `toml:"storage"`
			Replica string `toml:"replica"`
			Mail    string `toml:"mail"`
		} `toml:"providers"`
	} `toml:"site"`
	Mail struct {
		Host     string `toml:"host"`
		Port     int    `toml:"port"`
		Username string `toml:"username"`
		Password string `toml:"password"`
		From     string `toml:"from"`
		TLS      string `toml:"tls"`
	} `toml:"mail"`
}

func (cfg Config) pricing() (tube.Pricing, error) {
	p := tube.Prices
	b := cfg.Billing
	if b.FreeGB > 0 {
		p.FreeBytes = b.FreeGB * 1_000_000_000
	}
	if b.PerGBMonthUSD > 0 {
		p.PerGBMonth = tube.Micros(math.Round(b.PerGBMonthUSD * float64(tube.MicrosPerDollar)))
	}
	if b.GraceDays > 0 {
		p.GraceDays = b.GraceDays
	}
	if b.LockDays > 0 {
		p.LockDays = b.LockDays
	}
	if b.RefundDays > 0 {
		p.RefundDays = b.RefundDays
	}
	if b.RefundFeePct < 0 || b.RefundFeePct >= 100 {
		return p, fmt.Errorf("billing.refund_fee_percent must be between 0 and 100: %v", b.RefundFeePct)
	}
	p.RefundFeePct = b.RefundFeePct
	if p.LockDays <= p.GraceDays {
		return p, fmt.Errorf("billing.lock_days (%d) must be greater than billing.grace_days (%d)", p.LockDays, p.GraceDays)
	}
	if p.GraceDays <= p.LockWarningDays || p.LockDays-p.GraceDays <= p.DeleteWarnDays {
		return p, fmt.Errorf("billing.grace_days and billing.lock_days leave no room for warning e-mails")
	}
	return p, nil
}

func readConfig(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("failed to read config: %w", err)
	}
	if err := toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&cfg); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return cfg, fmt.Errorf("unknown settings in %s:\n%s", path, strict.String())
		}
		return cfg, err
	}
	if cfg.DB.Path == "" {
		cfg.DB.Path = "data/tube.db"
	}
	return cfg, nil
}

func (cfg Config) siteText() map[string]string {
	site := cfg.Site
	text := map[string]string{
		"service_name":             site.Name,
		"tos_operator":             site.Operator,
		"tos_contact":              site.Contact,
		"tos_security_contact":     site.SecurityContact,
		"tos_dmca_agent":           site.DMCAAgent,
		"tos_law":                  site.GoverningLaw,
		"tos_payment_methods":      site.PaymentMethods,
		"privacy_hosting_provider": site.Providers.Hosting,
		"privacy_storage_provider": site.Providers.Storage,
		"privacy_replica_provider": site.Providers.Replica,
		"privacy_mail_provider":    site.Providers.Mail,
	}
	if site.Name != "" {
		text["titleprefix"] = site.Name + " - "
	}
	return text
}
