# Windmill Deployment with Podman

This directory contains configuration to run [Windmill](https://www.windmill.dev) locally using Podman or Docker.

## Quick Start

### 1. Start Windmill Stack

Run the stack using `podman compose`:

```bash
podman compose -f windmill/podman-compose.yml up -d
```

Check running containers:
```bash
podman ps
```

### 2. Access Windmill UI

Open your browser to:
[http://localhost:8000](http://localhost:8000)

- Default superadmin credentials created on first launch:
  - **Email**: `admin@windmill.dev`
  - **Password**: `changeme`
*(You will be prompted to set a new password on first login)*

### 3. Generate API Token for API Gateway

1. Log into Windmill at `http://localhost:8000`.
2. Navigate to your user settings (bottom left user icon -> **User settings** -> **Tokens**).
3. Click **Add Token**, name it `api-gateway`, and copy the generated token.
4. Set this token in your API Gateway environment:
   ```bash
   export WINDMILL_TOKEN="<your-generated-token>"
   export WINDMILL_BASE_URL="http://127.0.0.1:8000"
   export WINDMILL_WORKSPACE="main"
   ```

### 4. Stopping Windmill

```bash
podman compose -f windmill/podman-compose.yml down
```
