from contextlib import asynccontextmanager

from fastapi import FastAPI

from db.database import init_db
from routers import admin, auth, webhooks


@asynccontextmanager
async def lifespan(_: FastAPI):
    init_db()
    yield


app = FastAPI(
    title="T3Z API Gateway",
    version="1.0.0",
    root_path="/apis",
    docs_url="/docs",
    redoc_url=None,
    openapi_url="/openapi.json",
    lifespan=lifespan,
)

app.include_router(auth.router)
app.include_router(admin.router)
app.include_router(webhooks.router)


@app.get("/health", tags=["System"])
async def health():
    return {"status": "ok"}