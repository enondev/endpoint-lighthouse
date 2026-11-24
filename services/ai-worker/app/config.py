from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    # Security
    internal_webhook_secret: str

    # Render Gateway Callback
    render_api_url: str

    # Cloudflare API Credentials
    cf_account_id: str
    cf_api_token: str
    cf_d1_database_id: str
    cf_kv_namespace_id: str

    # Workers AI Model
    cf_ai_model: str = "@cf/google/gemma-4-26b-a4b-it"

    # Runtime bounds
    max_diff_chars: int = 4500
    http_timeout_seconds: float = 25.0


settings = Settings()