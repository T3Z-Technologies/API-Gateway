from core.security import (
    create_workflow_token,
)


def issue_workflow_token(
    client_id: str,
    workflow_id: str,
) -> str:

    return create_workflow_token(
        client_id,
        workflow_id,
    )