//go:build unix

package durablefile

import "os"

// syncDir은 dir을 열어 fsync해서, 그 안에서 일어난 이름 바꾸기(rename)가
// 디스크에 반영됐음을 확정한다.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
