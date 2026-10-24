package web

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guregu/intertube/storage"
	"github.com/guregu/intertube/tube"
)

const (
	replicaWorkers    = 2
	replicaLease      = 15 * time.Minute
	replicaTick       = 30 * time.Second
	replicaExtendMax  = 500
	replicaVerifyMax  = 50
	replicaVerifyAge  = 7 * 24 * time.Hour
	snapshotPrefix    = "backups/db/"
	snapshotInterval  = 24 * time.Hour
	snapshotKeyLayout = "20060102T150405Z"
)

func StartReplication(ctx context.Context) {
	if !storage.Replicated() {
		return
	}
	go func() {
		last := latestSnapshot(ctx)
		ticker := time.NewTicker(replicaTick)
		defer ticker.Stop()
		for {
			now := time.Now().UTC()
			replicate(ctx, now)
			if now.Sub(last) >= snapshotInterval {
				if err := backupDatabase(ctx, now); err != nil {
					log.Println("backup:", err)
					alertAdmins("Database backup failed", "The daily database backup or its restore test failed:\n\n"+err.Error())
				}
				last = now
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func replicate(ctx context.Context, now time.Time) {
	if err := tube.ReconcileReplicas(ctx, now, storage.ReplicaDelay()); err != nil {
		log.Println("replication: reconcile:", err)
		return
	}
	var wg sync.WaitGroup
	for range replicaWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, ok, err := tube.ClaimReplicaJob(ctx, now, replicaLease)
				if err != nil {
					log.Println("replication: claim:", err)
					return
				}
				if !ok {
					return
				}
				runReplicaJob(ctx, job, now)
			}
		}()
	}
	wg.Wait()
	extendReplicaLocks(ctx, now)
	verifyReplicas(ctx, now)
}

func runReplicaJob(ctx context.Context, job tube.ReplicaJob, now time.Time) {
	var err error
	switch job.State {
	case tube.ReplicaCopy:
		err = copyToReplica(ctx, job.Key, now)
	case tube.ReplicaErase:
		if job.RetainUntil.After(now) {
			err = tube.PostponeReplica(ctx, job.Key, job.RetainUntil.Add(time.Minute))
			break
		}
		if err = storage.ReplicaBucket.EraseAll(ctx, job.Key); err == nil {
			err = tube.ErasedReplica(ctx, job.Key)
		}
	}
	if err != nil {
		log.Printf("replication: %s %s: %v", job.State, job.Key, err)
		if rerr := tube.RetryReplica(ctx, job.Key, job.Attempts, now, err); rerr != nil {
			log.Println("replication: retry:", rerr)
		}
	}
}

func replicaUntil(now time.Time) time.Time {
	if lock := storage.ReplicaLock(); lock > 0 {
		return now.Add(lock)
	}
	return time.Time{}
}

func copyToReplica(ctx context.Context, key string, now time.Time) error {
	f, err := os.CreateTemp(TempDir, "replica-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	size, err := storage.FilesBucket.Download(ctx, key, f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	until := replicaUntil(now)
	if err := storage.ReplicaBucket.PutLocked(ctx, key, f, size, until, storage.ReplicaClass()); err != nil {
		return err
	}
	return tube.CopiedReplica(ctx, key, until, now)
}

func extendReplicaLocks(ctx context.Context, now time.Time) {
	lock := storage.ReplicaLock()
	if lock <= 0 {
		return
	}
	keys, err := tube.ReplicasToExtend(ctx, now.Add(lock/2), replicaExtendMax)
	if err != nil {
		log.Println("replication: extend:", err)
		return
	}
	until := now.Add(lock)
	for _, key := range keys {
		if err := storage.ReplicaBucket.ExtendLock(ctx, key, until); err != nil {
			log.Printf("replication: extend %s: %v", key, err)
			continue
		}
		if err := tube.ExtendedReplica(ctx, key, until); err != nil {
			log.Println("replication: extend:", err)
		}
	}
}

func verifyReplicas(ctx context.Context, now time.Time) {
	keys, err := tube.ReplicasToVerify(ctx, now.Add(-replicaVerifyAge), replicaVerifyMax)
	if err != nil {
		log.Println("replication: verify:", err)
		return
	}
	for _, key := range keys {
		want, err := storage.FilesBucket.Size(ctx, key)
		if err != nil {
			continue
		}
		got, err := storage.ReplicaBucket.Size(ctx, key)
		if err != nil && !errors.Is(err, storage.ErrNoObject) {
			continue
		}
		ok := err == nil && got == want
		if !ok {
			log.Printf("replication: %s is missing or damaged on the replica, copying again", key)
		}
		if err := tube.VerifiedReplica(ctx, key, now, ok); err != nil {
			log.Println("replication: verify:", err)
		}
	}
}

func snapshotTime(key string) (time.Time, bool) {
	name := strings.TrimSuffix(strings.TrimPrefix(key, snapshotPrefix), ".db.enc")
	t, err := time.Parse(snapshotKeyLayout, name)
	return t, err == nil
}

func latestSnapshot(ctx context.Context) time.Time {
	keys, err := storage.ReplicaBucket.List(ctx, snapshotPrefix)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, key := range keys {
		if t, ok := snapshotTime(key); ok && t.After(latest) {
			latest = t
		}
	}
	return latest
}

func fileSum(path string) ([32]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, n, err
}

func backupDatabase(ctx context.Context, now time.Time) error {
	dir, err := os.MkdirTemp(TempDir, "backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	plainPath := filepath.Join(dir, "snapshot.db")
	if err := tube.Snapshot(ctx, plainPath); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	sum, _, err := fileSum(plainPath)
	if err != nil {
		return err
	}

	key := snapshotPrefix + now.UTC().Format(snapshotKeyLayout) + ".db.enc"
	encPath := filepath.Join(dir, "snapshot.enc")
	if err := encryptFile(encPath, plainPath, key); err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	enc, err := os.Open(encPath)
	if err != nil {
		return err
	}
	defer enc.Close()
	info, err := enc.Stat()
	if err != nil {
		return err
	}
	if err := storage.ReplicaBucket.PutLocked(ctx, key, enc, info.Size(), replicaUntil(now), ""); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	if err := restoreTest(ctx, key, sum, dir); err != nil {
		return fmt.Errorf("restore test: %w", err)
	}
	pruneSnapshots(ctx, now)
	return nil
}

func encryptFile(dst, src, key string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := tube.EncryptStream(out, in, tube.BackupKey(), key); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func restoreTest(ctx context.Context, key string, want [32]byte, dir string) error {
	encPath := filepath.Join(dir, "restore.enc")
	f, err := os.OpenFile(encPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	size, err := storage.ReplicaBucket.Download(ctx, key, f)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	reader, err := tube.NewDecryptingReader(tube.PlainSize(size), tube.BackupKey(), key, func(off, n int64) ([]byte, error) {
		buf := make([]byte, n)
		read, err := f.ReadAt(buf, off)
		if err == io.EOF {
			err = nil
		}
		return buf[:read], err
	})
	if err != nil {
		return err
	}
	restoredPath := filepath.Join(dir, "restored.db")
	out, err := os.OpenFile(restoredPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, reader); err != nil {
		out.Close()
		return fmt.Errorf("decrypt: %w", err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	if got, _, err := fileSum(restoredPath); err != nil || got != want {
		return fmt.Errorf("restored copy doesn't match the snapshot")
	}
	if _, err := tube.CheckDatabaseFile(ctx, restoredPath); err != nil {
		return err
	}
	return nil
}

func pruneSnapshots(ctx context.Context, now time.Time) {
	keep := max(storage.ReplicaLock(), 48*time.Hour)
	keys, err := storage.ReplicaBucket.List(ctx, snapshotPrefix)
	if err != nil {
		log.Println("backup: list:", err)
		return
	}
	sort.Strings(keys)
	for _, key := range keys {
		t, ok := snapshotTime(key)
		if !ok || now.Sub(t) < keep {
			continue
		}
		if err := storage.ReplicaBucket.EraseAll(ctx, key); err != nil {
			log.Printf("backup: erase %s: %v", key, err)
		}
	}
}

func alertAdmins(subject, body string) {
	if !mailEnabled() {
		return
	}
	for _, id := range AdminIDs {
		u, err := tube.GetUser(context.Background(), id)
		if err != nil {
			continue
		}
		if err := sendMail(u, subject, body); err != nil {
			log.Println("alert:", err)
		}
	}
}

func replicaDelayDays() int {
	return int(storage.ReplicaDelay() / (24 * time.Hour))
}

func eraseDays() int {
	if storage.Replicated() {
		return int(max(storage.ReplicaLock(), 48*time.Hour)/(24*time.Hour)) + 1
	}
	return 2
}
