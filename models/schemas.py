from pydantic import BaseModel, Field


class FirebaseLoginRequest(BaseModel):
    email: str
    password: str


class FirebaseLoginResponse(BaseModel):
    access_token: str
    token_type: str = "bearer"
    expires_in: int


class CreateClientRequest(BaseModel):
    client_id: str = Field(
        min_length=3,
        max_length=64,
        pattern=r"^[a-zA-Z0-9_-]+$",
    )

    client_name: str = Field(
        min_length=1,
        max_length=128,
    )


class CreateClientResponse(BaseModel):
    client_id: str
    client_name: str
    client_secret: str


class CreateWorkflowRequest(BaseModel):
    name: str = Field(
        min_length=1,
        max_length=128,
    )


class WorkflowResponse(BaseModel):
    client_id: str
    workflow_id: str
    n8n_workflow_id: str
    name: str
    webhook_url: str
    status: str


class TokenRequest(BaseModel):
    workflow_id: str = Field(
        min_length=1,
        max_length=128,
    )


class TokenResponse(BaseModel):
    access_token: str
    token_type: str = "Bearer"
    expires_in: int
    workflow_id: str