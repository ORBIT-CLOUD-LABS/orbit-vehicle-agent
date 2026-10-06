# Go 코드 컨벤션

## 기준

이 프로젝트는 [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)를 기준으로 한다.
아래 문서에 없는 내용은 기준 가이드를 따르고, 기준 가이드에도 없으면 다음을 참고한다.

- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)

이 문서에는 **기준 가이드와 다르게 가는 점**과 **기준 가이드가 정하지 않아 프로젝트에서 정한 점**만 적는다.
기준 가이드를 그대로 옮겨 적지 않는다.

포맷과 lint로 잡을 수 있는 규칙은 문서가 아니라 `gofmt`, `goimports`, `golangci-lint` 설정으로 강제한다.

## 패키지 구조

- 실행 바이너리의 진입점은 `cmd/<바이너리명>/main.go`에 둔다. `main`은 설정 로드, 의존성 조립, 실행만 맡는다.
- 외부에 공개하지 않는 코드는 모두 `internal/` 아래에 둔다. `pkg/`는 쓰지 않는다.
- 패키지는 계층(`service`, `repository`)이 아니라 기능 단위로 나눈다.
- `util`, `common`, `helper` 같은 이름의 패키지를 만들지 않는다.

## 에러 처리

- 에러에 맥락을 붙일 때는 `fmt.Errorf("동작 설명: %w", err)` 형식으로 감싼다. 메시지에 "failed to"를 붙이지 않는다 (기준 가이드와 같음).
- 호출자가 분기해야 하는 에러만 sentinel(`var ErrNotFound = errors.New(...)`)이나 커스텀 타입으로 노출한다. 그 외에는 감싸서 올려보내기만 한다.
- 에러는 **처리하거나 반환하거나 둘 중 하나만** 한다. 로그를 남긴 뒤 다시 반환하지 않는다.
- 에러를 로그로 남기는 곳은 최상위 경계(goroutine 루프, 요청 핸들러, `main`)로 한정한다.

## context

- I/O, 대기, 외부 호출이 있는 함수는 첫 번째 인자로 `ctx context.Context`를 받는다.
- `context.Context`를 struct 필드에 저장하지 않는다.
- `context.Background()`는 `main`과 테스트에서만 만든다. 그 외에는 전달받은 `ctx`를 쓴다.

## 동시성

- goroutine을 시작한 쪽이 종료까지 책임진다. 종료 신호(`ctx` 취소)와 종료 대기(`sync.WaitGroup` 또는 `errgroup.Group`) 수단이 없는 goroutine을 만들지 않는다.
- 여러 goroutine의 에러를 모아야 하면 `golang.org/x/sync/errgroup`을 쓴다.
- 채널은 송신하는 쪽에서 닫는다.
- 장시간 도는 컴포넌트는 `Run(ctx context.Context) error` 형태로 만들고, `ctx`가 취소되면 정리한 뒤 반환한다.

## 로깅

- 로깅은 `go.uber.org/zap`을 쓴다 (기준 가이드와 같음).
- `*zap.Logger`를 기본으로 쓰고, `SugaredLogger`는 쓰지 않는다.
- 로거는 전역 변수(`zap.L()`, `zap.ReplaceGlobals`)로 쓰지 않고 생성자로 주입한다.
- 구조화 필드의 키는 `snake_case`로 쓴다 (예: `zap.String("vehicle_id", id)`).
- 메시지는 소문자로 시작하고 마침표를 붙이지 않는다.

## 인터페이스

- 인터페이스는 구현하는 쪽이 아니라 **사용하는 쪽 패키지에** 정의한다.
- 인터페이스는 사용하는 메서드만 담아 작게 유지한다. 구현체가 하나뿐이고 교체나 테스트 대역이 필요 없으면 인터페이스를 만들지 않는다.
- 생성자는 인터페이스가 아니라 구체 타입을 반환한다.

## 테스트

- 테스트는 같은 디렉터리의 `_test.go`에 둔다. 공개 API만 검증할 때는 `package xxx_test`를 쓴다.
- 입력 케이스가 둘 이상이면 table-driven 테스트로 작성한다 (기준 가이드와 같음).
- 단언은 `github.com/stretchr/testify`의 `require`(실패 시 중단)와 `assert`(계속 진행)를 쓴다.
- 테스트 대역은 mock 생성 도구 없이 손으로 만든 fake를 우선한다.
- goroutine을 띄우는 패키지는 `go.uber.org/goleak`으로 누수를 확인한다.
