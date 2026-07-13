package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/xtawa/classing-backend/internal/config"
	"github.com/xtawa/classing-backend/internal/store"
)

type report struct {
	Missing        []string `json:"missing"`
	Corrupt        []string `json:"corrupt"`
	Orphans        []string `json:"orphans"`
	DeletedOrphans []string `json:"deletedOrphans,omitempty"`
}

func main() {
	deleteOrphans := flag.Bool("delete-orphans", false, "explicitly remove orphan APK files after reporting them")
	flag.Parse()
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	data, err := store.OpenNoMigrate(ctx, cfg.DatabaseDriver, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer data.Close()

	releases, _, err := data.ListReleases(ctx, 100000, 0)
	if err != nil {
		log.Fatal(err)
	}
	referenced := make(map[string]bool, len(releases))
	result := report{Missing: []string{}, Corrupt: []string{}, Orphans: []string{}}
	for _, item := range releases {
		name := filepath.Base(item.ArtifactStorageName)
		referenced[name] = true
		path := filepath.Join(cfg.ReleaseStorageDir, name)
		file, openErr := os.Open(path)
		if openErr != nil {
			result.Missing = append(result.Missing, name)
			continue
		}
		info, statErr := file.Stat()
		hash := sha256.New()
		_, hashErr := io.Copy(hash, file)
		_ = file.Close()
		if statErr != nil || hashErr != nil || info.Size() != item.ArtifactSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), item.ArtifactSHA256) {
			result.Corrupt = append(result.Corrupt, name)
		}
	}
	entries, err := os.ReadDir(cfg.ReleaseStorageDir)
	if err != nil && !os.IsNotExist(err) {
		log.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".apk") && !referenced[entry.Name()] {
			result.Orphans = append(result.Orphans, entry.Name())
			if *deleteOrphans {
				if err := os.Remove(filepath.Join(cfg.ReleaseStorageDir, entry.Name())); err != nil {
					log.Fatal(err)
				}
				result.DeletedOrphans = append(result.DeletedOrphans, entry.Name())
			}
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		log.Fatal(err)
	}
	if len(result.Missing) > 0 || len(result.Corrupt) > 0 {
		os.Exit(2)
	}
}
