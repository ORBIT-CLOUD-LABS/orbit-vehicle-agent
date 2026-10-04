## Related Issue

Closes #

## Summary

<!--
무엇을, 왜 변경했는지 2~4개의 불릿으로 작성합니다.
구현 세부사항보다 차량 Agent의 동작과 OTA 흐름에 미치는 영향을 우선 작성합니다.

필요한 경우 아래 항목을 Summary의 불릿으로 추가합니다.
- Agent Behavior: 차량 등록, 상태 보고, 매니페스트 조회, 패키지 다운로드·검증, 설치 모사, 결과 보고 변경
- Protocol Changes: OTA Server API, 요청·응답 형식, 인증 방식 또는 하위 호환성 변경
- Configuration Changes: Server URL, 보고 주기, timeout, retry, 동시 실행 수 또는 실패 주입 옵션 변경
- Dependency Changes: go.mod 또는 go.sum 변경
-->

-

## Verification

<!--
실행한 명령과 결과를 작성합니다.
예: go test ./... 통과, Mock OTA Server 연동 시 상태 보고부터 결과 보고까지 정상 완료

관련 기능을 변경한 경우에만 아래 검증 결과를 추가합니다.
- 동시성: go test -race ./...
- 통신 제어: timeout, retry, 취소 및 실패 보고
- 패키지 처리: checksum 검증 및 손상 파일 처리
- 운영 설정: 환경 변수, 실행 방법 및 로그의 민감정보 노출 여부
-->

-

- [ ] `gofmt`를 적용했습니다.
- [ ] `go test ./...`가 통과했습니다.
- [ ] `go build ./...`가 통과했습니다.
- [ ] 변경한 Agent 동작을 직접 확인했습니다.
- [ ] OTA Server API 변경 여부를 확인했습니다.

<!--
## To Reviewer

리뷰 시 중점적으로 확인할 부분, OTA Server와의 호환성, 동시성 위험,
후속 작업 또는 알려진 제한 사항이 있을 때만 이 블록을 주석에서 꺼내 작성합니다.
-->
