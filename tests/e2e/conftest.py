import functools
import http.server
import socket
import threading
from pathlib import Path
import pytest


class QuietHTTPRequestHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, format, *args):
        pass


@pytest.fixture(scope="session")
def frontend_base_url():
    repo_root = Path(__file__).resolve().parent.parent.parent
    frontend_dir = repo_root / "frontend"
    handler = functools.partial(QuietHTTPRequestHandler, directory=str(frontend_dir))

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        _, port = s.getsockname()

    server = http.server.ThreadingHTTPServer(("127.0.0.1", port), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()

    yield f"http://127.0.0.1:{port}"

    server.shutdown()
    server.server_close()