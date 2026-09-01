import httpx

from fastapi import (
    APIRouter,
    HTTPException,
    Request,
)
from fastapi.responses import Response

from core.config import settings
from core.security import (
    verify_workflow_token,
)
from db.database import get_db


router = APIRouter(
    prefix="/webhooks",
    tags=["Webhooks"],
)


HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}


def ensure_active_workflow(
    client_id: str,
    workflow_id: str,
) -> None:

    conn = get_db()

    row = conn.execute(
        """
        SELECT workflow_id
        FROM client_workflows
        WHERE client_id = ?
          AND workflow_id = ?
          AND status = 'active'
        """,
        (
            client_id,
            workflow_id,
        ),
    ).fetchone()

    conn.close()

    if not row:
        raise HTTPException(
            status_code=403,
            detail="Workflow is not authorized",
        )


@router.api_route(
    "/{workflow_id}",
    methods=[
        "POST",
        "PUT",
        "PATCH",
        "DELETE",
    ],
)
async def gateway(
    workflow_id: str,
    request: Request,
):

    authorization = request.headers.get(
        "Authorization",
        "",
    )

    if not authorization.startswith(
        "Bearer "
    ):
        raise HTTPException(
            status_code=401,
            detail="Unauthorized",
        )

    token = authorization[7:].strip()

    claims = verify_workflow_token(
        token,
        workflow_id,
    )

    client_id = claims["sub"]

    ensure_active_workflow(
        client_id,
        workflow_id,
    )

    body = await request.body()

    headers = {}

    for key, value in request.headers.items():

        lower = key.lower()

        if lower in HOP_BY_HOP_HEADERS:
            continue

        if lower in {
            "authorization",
            "host",
            "content-length",
        }:
            continue

        headers[key] = value

    # These headers are trusted because
    # the caller cannot supply the originals.
    headers[
        "X-T3Z-Client-Id"
    ] = client_id

    headers[
        "X-T3Z-Workflow-Id"
    ] = workflow_id

    target_url = (
    f"{settings.n8n_base_url}/"
    f"{settings.n8n_webhook_prefix}/"
    f"{workflow['webhook_path']}"
)

    try:
        async with httpx.AsyncClient(
            timeout=30.0
        ) as client:

            upstream = await client.request(
                method=request.method,
                url=target_url,
                params=request.query_params,
                headers=headers,
                content=body,
            )

    except httpx.RequestError as exc:
        raise HTTPException(
            status_code=502,
            detail="n8n upstream unavailable",
        ) from exc

    response_headers = {
        key: value
        for key, value in upstream.headers.items()
        if key.lower()
        not in HOP_BY_HOP_HEADERS
    }

    return Response(
        content=upstream.content,
        status_code=upstream.status_code,
        headers=response_headers,
        media_type=upstream.headers.get(
            "content-type"
        ),
    )

    def get_workflow(
        client_id: str,
        workflow_id: str,
    ) -> dict:

        conn = get_db()

        row = conn.execute(
            """
            SELECT
                client_id,
                workflow_id,
                n8n_workflow_id,
                webhook_path,
                status
            FROM client_workflows
            WHERE client_id = ?
            AND workflow_id = ?
            AND status = 'active'
            """,
            (
                client_id,
                workflow_id,
            ),
        ).fetchone()

        conn.close()

        if not row:
            raise HTTPException(
                status_code=403,
                detail="Workflow not authorized",
            )

        return dict(row)