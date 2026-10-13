// Package durablefile는 임시 파일에 쓴 내용을 fsync로 디스크에 반영한 뒤
// 최종 경로로 원자적으로 이름을 바꾸고, 그 이름 바꾸기 자체도 상위
// 디렉터리를 fsync해 확정한다. rename만으로는 원자성(반쯤 쓴 파일이
// 보이지 않음)은 보장되지만, 전원 차단 시 내구성(디스크에 실제로
// 반영됐는지)은 보장되지 않는다.
package durablefile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile은 data를 tmpPath에 새로 써서 fsync하고, finalPath로 원자적으로
// 이름을 바꾼 뒤 상위 디렉터리를 fsync해 그 이름 바꾸기를 확정한다.
// tmpPath와 finalPath는 같은 디렉터리에 있어야 한다. 실패하면 tmpPath를
// 지운다.
func WriteFile(tmpPath, finalPath string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("임시 파일 생성: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("임시 파일 쓰기: %w", err)
	}

	if err := SyncAndClose(f); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// SyncAndClose는 f에 쓴 내용을 fsync로 디스크에 반영한 뒤 닫는다. 호출자가
// f에 직접 쓴 뒤(io.Copy 등) finalPath로 이름을 바꾸기 전에 호출해야 한다.
func SyncAndClose(f *os.File) error {
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("임시 파일 fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("임시 파일 닫기: %w", err)
	}
	return nil
}

// Rename은 tmpPath를 finalPath로 원자적으로 이름을 바꾸고, 상위 디렉터리를
// fsync해 그 이름 바꾸기가 디스크에 반영됐음을 확정한다. tmpPath는 이미
// SyncAndClose로 닫혀 있어야 한다.
func Rename(tmpPath, finalPath string) error {
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("이름 바꾸기: %w", err)
	}
	if err := syncDir(filepath.Dir(finalPath)); err != nil {
		return fmt.Errorf("상위 디렉터리 fsync: %w", err)
	}
	return nil
}
