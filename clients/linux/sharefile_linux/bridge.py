"""Private stdin/stdout protocol. Credentials never enter command arguments."""
import json
import selectors
import subprocess
import threading
import time


class ClientError(Exception):
    pass


class Cancelled(ClientError):
    def __init__(self):
        super().__init__("Операция приостановлена")


class BridgeCall:
    def __init__(self, helper):
        self.helper = str(helper)
        self.cancelled = threading.Event()

    def cancel(self):
        self.cancelled.set()

    def run(self, request, progress=lambda job: None):
        if self.cancelled.is_set():
            raise Cancelled()
        process = subprocess.Popen([self.helper], stdin=subprocess.PIPE,
                                   stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        try:
            process.stdin.write(json.dumps(request, ensure_ascii=False).encode() + b"\n")
            process.stdin.close()
            # Use unbuffered reads: BufferedReader may prefetch a final event past select().
            buffer = b""
            final = None
            deadline = time.monotonic() + 16 * 60
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                while final is None:
                    if self.cancelled.is_set():
                        raise Cancelled()
                    if time.monotonic() > deadline:
                        raise ClientError("Сервер не ответил вовремя. Попробуйте продолжить загрузку")
                    if not selector.select(0.1):
                        continue
                    chunk = process.stdout.read1(65536)
                    if not chunk:
                        raise ClientError("Ядро передачи завершилось без результата")
                    buffer += chunk
                    if len(buffer) > 16 << 20:
                        raise ClientError("Слишком большой ответ ядра передачи")
                    while b"\n" in buffer:
                        line, buffer = buffer.split(b"\n", 1)
                        try:
                            event = json.loads(line)
                        except (ValueError, UnicodeError):
                            raise ClientError("Некорректный ответ ядра передачи") from None
                        if not isinstance(event, dict):
                            raise ClientError("Некорректный ответ ядра передачи")
                        if event.get("type") == "progress":
                            progress(event["job"])
                            deadline = time.monotonic() + 16 * 60
                        elif event.get("type") in ("result", "error"):
                            final = event
                        else:
                            raise ClientError("Неизвестный ответ ядра передачи")
            process.wait(timeout=5)
            if final["type"] == "error":
                raise ClientError(final.get("error", "Ошибка передачи"))
            return final.get("result")
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            if not process.stdin.closed:
                process.stdin.close()
            process.stdout.close()


def safe_message(error):
    if isinstance(error, ClientError):
        return str(error)
    if isinstance(error, OSError):
        return "Не удалось прочитать файл или сохранить очередь. Проверьте доступ и свободное место"
    return "Не удалось выполнить операцию. Проверьте подключение и попробуйте снова"
