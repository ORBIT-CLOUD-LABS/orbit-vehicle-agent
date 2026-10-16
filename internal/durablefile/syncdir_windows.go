//go:build windows

package durablefile

// Windows에서는 디렉터리 핸들에 FlushFileBuffers를 적용할 수 없어 디렉터리
// fsync를 지원하지 않는다. 이 에이전트의 배포 대상은 Linux 컨테이너(
// Dockerfile 참고)이므로 Windows에서는 디렉터리 fsync를 건너뛴다.
func syncDir(dir string) error {
	return nil
}
