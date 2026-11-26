FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PORT=8000

WORKDIR /app

# Create non-root user for hardened container execution
RUN groupadd -r specwatch && useradd -r -g specwatch specwatch

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY --chown=specwatch:specwatch app/ ./app/

USER specwatch

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8000/health')" || exit 1

CMD ["sh", "-c", "uvicorn app.main:app --host 0.0.0.0 --port ${PORT} --workers 1"]