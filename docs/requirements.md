# 요구사항 참조

요구사항의 원본(source of truth)은 orbit 레포에 있다. 이 레포에 복사하지 않는다.

## 위치

| 항목 | 값 |
| --- | --- |
| 레포 | https://github.com/ORBIT-CLOUD-LABS/orbit |
| 경로 | `docs/` |


## 읽는 방법

1. 로컬 경로가 있으면 읽기 전에 `git -C ../orbit pull` 로 최신화한다.
2. 로컬에 없으면 GitHub 에서 읽는다.

   ```bash
   # 파일 목록
   gh api repos/ORBIT-CLOUD-LABS/orbit/contents/docs/requirements --jq '.[].name'
   # 파일 내용
   gh api repos/ORBIT-CLOUD-LABS/orbit/contents/docs/requirements/<파일명> -H 'Accept: application/vnd.github.raw'
   ```

3. 둘 다 안 되면 추측해서 구현하지 말고 사용자에게 요구사항을 요청한다.

## 규칙

- 기능을 구현하기 전에 관련 요구사항을 먼저 읽는다.
- 요구사항 문서는 읽기만 한다. orbit 레포를 수정하지 않는다.
- PR Summary 에 요구사항 ID 를 적는다. (예: `REQ-ORDER-003`)
- 테스트에는 요구사항 ID 를 서브테스트 이름이나 테스트 함수 주석에 적는다. Go 테스트 함수 이름에는 `-` 를 쓸 수 없기 때문이다. (예: `t.Run("REQ-ORDER-003 잔액 부족이면 거절", ...)`)
- 요구사항이 모호하거나 코드와 다르면 임의로 해석하지 말고 질문한다.
