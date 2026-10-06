# Git 컨벤션 참조

Git 작업 규칙의 원본(source of truth)은 Organization 공통 `CONTRIBUTING.md` 에 있다. 이 레포에 복사하지 않는다.

## 위치

| 항목 | 값 |
| --- | --- |
| 레포 | https://github.com/ORBIT-CLOUD-LABS/.github |
| 경로 | `CONTRIBUTING.md` |

## 읽는 방법

1. 로컬 경로가 있으면 읽기 전에 `git -C ../.github pull` 로 최신화한다.
2. 로컬에 없으면 GitHub 에서 읽는다.

   ```bash
   gh api repos/ORBIT-CLOUD-LABS/.github/contents/CONTRIBUTING.md -H 'Accept: application/vnd.github.raw'
   ```

3. 둘 다 안 되면 추측해서 진행하지 말고 사용자에게 규칙을 확인한다.

## 언제 읽는가

- Issue 생성, Branch 생성, 커밋, Push, PR 생성·리뷰 등 Git 작업을 시작하기 전.
- 규칙이 기억과 다르거나 확실하지 않을 때.

## 규칙

- 작업 흐름은 `Issue → Branch → Commit → Push → Pull Request → Review → Merge` 순서를 지킨다. 단계를 건너뛰지 않는다.
- Issue 는 이 레포(`orbit-vehicle-agent`) 범위의 작업만 다룬다. 다른 레포 변경이 필요하면 레포별 Issue·PR 로 나누자고 제안한다.
- Issue 를 만들 때는 Issue Type(`Feature`, `Bug`, `Task`)과 `area:*` Label 을 하나 고른다. `work:*`, `needs:decision`, `cross-repo`, `risk:security` 는 필요할 때만 붙인다.
- Secret, Credential, 개인정보를 커밋과 PR 에 넣지 않는다.
- Merge 는 사람이 한다. 에이전트는 Approve·Conversation 해결·Merge 를 대신하지 않는다.
- 원본 문서는 읽기만 한다. `.github` 레포를 수정하지 않는다.
