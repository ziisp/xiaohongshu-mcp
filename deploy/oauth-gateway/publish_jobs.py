"""Durable, at-most-once dispatch for the domain gateway's publishing tools.

No automatic replay: interrupted dispatch is unknown until checked on the platform.
The single-process lock is essential: two workers must not recover each other's jobs.
"""
import asyncio
import fcntl
import hashlib
import json
import os
import sqlite3
import time
import uuid
from pathlib import Path

PUBLISH_TOOLS = frozenset({"publish_content", "publish_with_video"})
TERMINAL = frozenset({"submitted", "failed", "unknown"})
MESSAGES = {
    "queued": "已受理，尚未提交。请调用 get_publish_status，不能当作发布成功或重新发布。",
    "running": "正在处理，尚未确认提交。请继续查询同一 publication_id，不要重发。",
    "submitted": "已确认提交；公开及审核状态待确认。不要重复发布。",
    "failed": "提交前失败，未点击发布。修正原因后可创建新请求。",
    "unknown": "提交结果未知。请先检查创作平台笔记管理，不要自动重新发布。",
}


def tool_result(data, *, error=False):
    return {"isError": error, "structuredContent": data,
            "content": [{"type": "text", "text": json.dumps(data, ensure_ascii=False)}]}


def canonical(tool, arguments):
    args = dict(arguments)
    key = args.pop("idempotency_key", None)
    if key is not None and (not isinstance(key, str) or not 1 <= len(key) <= 128 or
                            any(ord(c) < 33 or ord(c) > 126 for c in key)):
        raise ValueError("idempotency_key must be 1-128 printable non-space ASCII characters")
    # Match the Go argument defaults without changing caller-supplied values.
    normalized = dict(args)
    normalized.setdefault("ai_generated", True)
    for name, default in (("tags", []), ("products", []), ("schedule_at", ""),
                          ("visibility", "公开可见")):
        if name not in normalized or normalized[name] == "":
            normalized[name] = default
    if tool == "publish_content":
        normalized.setdefault("is_original", False)
    encoded = json.dumps([tool, normalized], sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    fingerprint = hashlib.sha256(encoded.encode()).hexdigest()
    key_hash = hashlib.sha256(key.encode()).hexdigest() if key is not None else None
    return args, fingerprint, key_hash


class PublishJobs:
    def __init__(self, path, *, max_pending=8):
        path = Path(path)
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.lock = open(str(path) + ".lock", "a")
        os.chmod(str(path) + ".lock", 0o600)
        try:
            fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            self.lock.close()
            raise RuntimeError("Publishing state is already owned by another process") from None
        self.db = sqlite3.connect(path)
        os.chmod(path, 0o600)
        self.db.row_factory = sqlite3.Row
        self.db.execute("PRAGMA journal_mode=DELETE")
        self.db.execute("PRAGMA synchronous=FULL")
        self.db.execute("""CREATE TABLE IF NOT EXISTS jobs (
            id TEXT PRIMARY KEY, owner TEXT NOT NULL, tool TEXT NOT NULL,
            fingerprint TEXT NOT NULL, key_hash TEXT, status TEXT NOT NULL,
            created REAL NOT NULL, updated REAL NOT NULL, detail TEXT,
            UNIQUE(owner, key_hash))""")
        self.db.execute("CREATE INDEX IF NOT EXISTS job_owner_fingerprint ON jobs(owner, fingerprint)")
        self.db.execute("CREATE TABLE IF NOT EXISTS request_keys (owner TEXT, key_hash TEXT, job_id TEXT, PRIMARY KEY(owner,key_hash))")
        self.db.execute("INSERT OR IGNORE INTO request_keys SELECT owner,key_hash,id FROM jobs WHERE key_hash IS NOT NULL")
        self.db.execute("UPDATE jobs SET status='unknown', updated=? WHERE status IN ('queued','running')", (time.time(),))
        self.db.commit()
        self.max_pending = max_pending
        self.tasks = set()
        self.serial = asyncio.Semaphore(1)
        self.closed = False

    def _view(self, row):
        status = row["status"]
        data = {"publication_id": row["id"], "tool": row["tool"], "status": status,
                "retry_safe": status == "failed", "message": MESSAGES[status],
                "created_at": row["created"], "updated_at": row["updated"]}
        if row["detail"]:
            data["detail"] = row["detail"]
        return data

    def get(self, owner, job_id):
        row = self.db.execute("SELECT * FROM jobs WHERE id=? AND owner=?", (job_id, owner)).fetchone()
        if row is None:
            raise ValueError("Publication not found for the current account")
        return self._view(row)

    def recent(self, owner):
        return [self._view(row) for row in self.db.execute(
            "SELECT * FROM jobs WHERE owner=? ORDER BY created DESC LIMIT 10", (owner,))]

    def _update(self, job_id, status, detail=None):
        # Keep only a bounded diagnostic, never credentials or the original arguments.
        self.db.execute("UPDATE jobs SET status=?, updated=?, detail=? WHERE id=?",
                        (status, time.time(), detail[:12000] if detail else None, job_id))
        self.db.commit()

    async def submit(self, owner, tool, arguments, execute):
        if self.closed:
            raise ValueError("Publishing gateway is stopping; query recent publications before retrying")
        args, fingerprint, key_hash = canonical(tool, arguments)
        row = None
        if key_hash is not None:
            row = self.db.execute("""SELECT jobs.* FROM jobs JOIN request_keys ON jobs.id=request_keys.job_id
                WHERE request_keys.owner=? AND request_keys.key_hash=?""", (owner, key_hash)).fetchone()
            if row is not None and row["fingerprint"] != fingerprint:
                raise ValueError("idempotency_key is already bound to different publishing arguments")
        if row is None:
            # An added/changed key cannot authorize replay of an uncertain side effect.
            row = self.db.execute("""SELECT * FROM jobs WHERE owner=? AND fingerprint=? AND
                status IN ('queued','running','unknown') ORDER BY created DESC LIMIT 1""", (owner,fingerprint)).fetchone()
        if row is None and key_hash is None:
            row = self.db.execute("""SELECT * FROM jobs WHERE owner=? AND fingerprint=? AND
                (status IN ('queued','running','unknown') OR (status='submitted' AND updated>?))
                ORDER BY created DESC LIMIT 1""", (owner, fingerprint, time.time()-600)).fetchone()
        if row is not None:
            if key_hash is not None:
                self.db.execute("INSERT OR IGNORE INTO request_keys VALUES (?,?,?)", (owner,key_hash,row["id"]))
                self.db.commit()
            return self._view(row)
        pending = self.db.execute("SELECT count(*) FROM jobs WHERE status IN ('queued','running')").fetchone()[0]
        if pending >= self.max_pending:
            raise ValueError("Publishing queue is full; query existing publications before submitting more")
        job_id, now = "pub_" + uuid.uuid4().hex, time.time()
        self.db.execute("INSERT INTO jobs VALUES (?,?,?,?,?,?,?,?,NULL)",
                        (job_id, owner, tool, fingerprint, key_hash, "queued", now, now,))
        if key_hash is not None:
            self.db.execute("INSERT INTO request_keys VALUES (?,?,?)", (owner,key_hash,job_id))
        self.db.commit()  # Durable acceptance BEFORE any outbound side effect.
        task = asyncio.create_task(self._run(job_id, args, execute))
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)
        return self.get(owner, job_id)

    async def _run(self, job_id, args, execute):
        try:
            async with self.serial:
                self._update(job_id, "running")
                result = await execute(args)
                state = result.get("structuredContent", {})
                status = "unknown"
                if isinstance(state, dict):
                    if state.get("status") == "submitted" and not result.get("isError"):
                        status = "submitted"
                    elif (state.get("status") == "failed" and state.get("phase") == "pre_submit" and
                          state.get("retry_safe") is True and result.get("isError") is True):
                        status = "failed"
                detail = "\n".join(item.get("text", "") for item in result.get("content", [])
                                   if isinstance(item, dict) and item.get("type") == "text")
                self._update(job_id, status, detail)
        except asyncio.CancelledError:
            self._update(job_id, "unknown")
            raise
        except Exception:
            # A transport error does not establish whether the platform received the click.
            self._update(job_id, "unknown")

    async def wait(self, owner, job_id, seconds):
        deadline = asyncio.get_running_loop().time() + seconds
        while True:
            view = self.get(owner, job_id)
            if view["status"] in TERMINAL or asyncio.get_running_loop().time() >= deadline:
                return view
            await asyncio.sleep(min(0.1, max(0, deadline-asyncio.get_running_loop().time())))

    async def close(self):
        self.closed = True
        tasks = list(self.tasks)
        for task in tasks:
            task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        self.db.execute("UPDATE jobs SET status='unknown', updated=? WHERE status IN ('queued','running')", (time.time(),))
        self.db.commit()
        self.db.close()
        self.lock.close()
