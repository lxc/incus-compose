package client

import (
	"context"
	"time"
)

// VolumeInfos represents either a Source or a Backup in a BackupEntry.
type VolumeInfos struct {
	Project string `json:"project"`
	Pool    string `json:"pool"`
	Name    string `json:"name"`
}

// BackupVolume represents a single volume in a backup run.
type BackupVolume struct {
	Source VolumeInfos `json:"source"`
	Backup VolumeInfos `json:"backup"`
}

// BackupConfig holds backup configuration from x-incus-compose.backup.
type BackupConfig struct {
	MetaVolume string `json:"meta_volume"`
	Timestamp  string `json:"timestamp"`
	Name       string `json:"name"`
	Pool       string `json:"pool"`
}

func BackupManifestVolume(ctx context.Context, bc *Client, cfg BackupConfig) (*StorageVolume, error) {
	rBMVol, err := bc.Resource(KindStorageVolume, cfg.MetaVolume, &StorageVolumeConfig{Pool: cfg.Pool})
	if err != nil {
		return nil, err
	}

	err = RunAction(ctx, rBMVol, ActionEnsure, OptionCreate())
	if err != nil {
		return nil, err
	}

	bMVol, ok := rBMVol.(*StorageVolume)
	if !ok {
		return nil, ErrUnknownResource.WithText("while converting a backup resource to a StorageVolume")
	}

	return bMVol, nil
}

// BackupLock creates backupLock which you MUST release with Unlock().
func BackupLock(ctx context.Context, bc *Client, cfg BackupConfig, stale time.Duration, lockName string) (*VolumeLock, error) {
	bc.LogDebug("Locking", "cfg", cfg, "lock_name", lockName)

	bMVol, err := BackupManifestVolume(ctx, bc, cfg)
	if err != nil {
		return nil, err
	}
	sc, err := bMVol.SFTP()
	if err != nil {
		return nil, err
	}

	lock, err := bMVol.Lock(ctx, sc, lockName, stale)
	if err != nil {
		return nil, err
	}

	return lock, err
}
