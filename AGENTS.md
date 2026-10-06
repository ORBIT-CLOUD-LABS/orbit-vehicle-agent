# AGENTS.md

orbit-vehicle-agent 에서 Go 코드를 작성·수정·리뷰하는 AI 에이전트용 지침.
세부 규칙은 `docs/` 에 있고, 이 문서는 작업 순서와 어떤 문서를 언제 볼지를 정한다.

## 요구사항

요구사항의 원본(source of truth)은 orbit 레포에 있다. 이 레포에 복사하지 않는다.
기능 구현 전에 읽는 방법과 규칙은 [docs/requirements.md](docs/requirements.md).

## 작업 전 읽을 문서

| 상황 | 문서 |
| --- | --- |
| 기능 구현 (요구사항 확인) | [docs/requirements.md](docs/requirements.md) |
| Go 코드 작성·리뷰 (항상) | [docs/code-convention.md](docs/code-convention.md) |
| 코드 컨벤션 문서에 없는 스타일 판단 | [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md) |
| Issue 등록, Branch 생성, 커밋, Push, PR 작성·리뷰 | [docs/git-conventions.md](docs/git-conventions.md) |

문서끼리 충돌하면 `code-convention.md` > Uber Go Style Guide 순으로 따른다.

## 핵심 규칙 요약

문서를 다 읽지 못했더라도 아래는 반드시 지킨다.

- 진입점은 `cmd/<바이너리명>/main.go`, 나머지 코드는 `internal/` 에 기능 단위로 둔다. `util` / `common` 패키지 금지.
- 에러는 `fmt.Errorf("동작 설명: %w", err)` 로 감싼다. 처리하거나 반환하거나 둘 중 하나만 한다.
- I/O 가 있는 함수는 첫 인자로 `ctx context.Context` 를 받는다. struct 필드에 저장하지 않는다.
- goroutine 을 시작한 쪽이 종료까지 책임진다. 취소·대기 수단 없는 goroutine 금지.
- 로깅은 생성자로 주입한 `*zap.Logger` 만 쓴다. 전역 로거·`SugaredLogger` 금지.
- 인터페이스는 사용하는 쪽에 작게 정의하고, 생성자는 구체 타입을 반환한다.

## 작업 방식

- 기존 코드의 구조·네이밍·주석 밀도를 먼저 확인하고 그에 맞춘다.
- 요청 범위 밖의 리팩터링이나 파일 정리는 하지 않는다. 필요해 보이면 제안만 한다.
- 기능 구현은 테스트와 함께 작성한다. 버그 수정은 재현 테스트를 먼저 쓴다.
- 작업을 마치기 전에 아래 검증을 실행하고 결과를 그대로 보고한다. 실패를 숨기지 않는다.

## 빌드·검증

```bash
gofmt -l .            # 포맷 검사 (출력이 없어야 한다)
go vet ./...          # 정적 검사
go test -race ./...   # 테스트 (race detector 포함)
go build ./...        # 전체 빌드
```

- 포맷은 손으로 맞추지 않고 `gofmt` / `goimports` 에 맡긴다.