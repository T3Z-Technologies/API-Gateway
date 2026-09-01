from typing import Any

import httpx

from core.config import settings


class N8NError(Exception):
    pass


class N8NClient:

    def __init__(self):
        self.base_url = settings.n8n_base_url
        self.api_key = settings.n8n_api_key

    async def request(
        self,
        method: str,
        path: str,
        **kwargs: Any,
    ) -> httpx.Response:

        url = (
            f"{self.base_url}"
            f"/api/v1/{path.lstrip('/')}"
        )

        headers = dict(
            kwargs.pop("headers", {})
        )

        headers["X-N8N-API-KEY"] = (
            self.api_key
        )

        try:
            async with httpx.AsyncClient(
                timeout=30.0
            ) as client:

                response = await client.request(
                    method,
                    url,
                    headers=headers,
                    **kwargs,
                )

        except httpx.RequestError as exc:
            raise N8NError(
                "Could not connect to n8n"
            ) from exc

        if response.status_code >= 400:
            raise N8NError(
                f"n8n returned "
                f"{response.status_code}: "
                f"{response.text}"
            )

        return response

    async def create_project(
        self,
        *,
        name: str,
    ) -> dict:

        response = await self.request(
            "POST",
            "/projects",
            json={
                "name": name,
            },
        )

        return response.json()

    async def create_folder(
        self,
        *,
        name: str,
    ) -> dict:

        response = await self.request(
            "POST",
            f"/projects/{settings.n8n_project_id}/folders",
            json={
                "name": name,
            },
        )

        return response.json()

    async def delete_project(
        self,
        project_id: str,
    ) -> None:

        try:

            await self.request(
                "DELETE",
                f"/projects/{project_id}",
            )

        except N8NError:
            pass

    async def delete_folder(
    self,
    folder_id: str,
    ) -> None:

        try:

            await self.request(
                "DELETE",
                f"/folders/{folder_id}",
            )

        except N8NError:
            pass

    async def create_workflow(
        self,
        *,
        name: str,
        webhook_path: str,
        parent_folder_id: str,
    ) -> dict:

        payload = {
            "name": name,

            "nodes": [
                {
                    "id": "t3z-webhook",
                    "name": "T3Z Webhook",
                    "type": "n8n-nodes-base.webhook",
                    "typeVersion": 2,
                    "position": [
                        300,
                        300,
                    ],
                    "parameters": {
                        "httpMethod": "POST",
                        "path": webhook_path,
                        "responseMode": "lastNode",
                    },
                }
            ],

            "connections": {},

            "settings": {
                "executionOrder": "v1",
            },

            "parentFolderId": parent_folder_id,
        }

        response = await self.request(
            "POST",
            "/workflows",
            json=payload,
        )

        return response.json()

    async def activate_workflow(
        self,
        workflow_id: str,
    ) -> None:

        await self.request(
            "POST",
            f"/workflows/"
            f"{workflow_id}/activate",
        )

    async def delete_workflow(
        self,
        workflow_id: str,
    ) -> None:

        try:
            await self.request(
                "DELETE",
                f"/workflows/"
                f"{workflow_id}",
            )

        except N8NError:
            # Cleanup is best-effort.
            pass


n8n = N8NClient()