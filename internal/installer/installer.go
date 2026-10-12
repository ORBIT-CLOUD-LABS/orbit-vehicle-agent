// Package installer는 검증된 펌웨어 파일을 차량의 단일 설치 위치에 반영한다.
package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FailureReason은 설치 실패의 종류다. 업데이트 결과를 보고하는 호출자
// (예: 에이전트 상태 머신)가 자기 실패 사유로 변환해 쓴다. 이 패키지는 그
// 사유 정의에 직접 의존하지 않는다.
type FailureReason string

const FailureInstallFailed FailureReason = "INSTALL_FAILED"

// Error는 설치 실패를 FailureReason과 함께 감싼다.
type Error struct {
	Reason FailureReason
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Result는 설치를 마친 펌웨어 이미지 정보다.
type Result struct {
	Path string
}

// Installer는 검증된 펌웨어를 DataDir/firmware/current.bin에 반영한다.
type Installer struct {
	dataDir string
}

// New는 dataDir 아래에 설치하는 Installer를 만든다.
func New(dataDir string) *Installer {
	return &Installer{dataDir: dataDir}
}

// Install은 sourcePath의 검증된 펌웨어를 DataDir/firmware/current.bin에
// 복사한다. 기존 파일은 임시 파일과 os.Rename으로 원자적으로 교체한다.
//
// Install은 멱등이다. 이전 설치 기록을 메모리에 두지 않으므로, 프로세스
// 재시작 후를 포함해 같은 sourcePath 내용으로 다시 호출하면 current.bin을
// 같은 바이트로 덮어쓰고 같은 Result를 반환한다.
func (i *Installer) Install(ctx context.Context, sourcePath string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	src, err := os.Open(sourcePath)
	if err != nil {
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("open verified firmware: %w", err)}
	}
	defer src.Close()

	dir := filepath.Join(i.dataDir, "firmware")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Result{}, fmt.Errorf("create firmware dir: %w", err)
	}

	path := filepath.Join(dir, "current.bin")
	tmpPath := path + ".tmp"

	dst, err := os.Create(tmpPath)
	if err != nil {
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("create temp install file: %w", err)}
	}

	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()

	if copyErr != nil {
		os.Remove(tmpPath)
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("copy firmware to install location: %w", copyErr)}
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return Result{}, &Error{Reason: FailureInstallFailed, Err: fmt.Errorf("close temp install file: %w", closeErr)}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return Result{}, fmt.Errorf("rename installed file: %w", err)
	}

	return Result{Path: path}, nil
}
