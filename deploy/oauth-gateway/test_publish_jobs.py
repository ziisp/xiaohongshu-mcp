import asyncio
import tempfile
import unittest
from pathlib import Path

from publish_jobs import PublishJobs


class PublishJobTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name)/"jobs.sqlite3"
        self.jobs = PublishJobs(self.path)
        self.calls = 0
        self.started = asyncio.Event()
        self.release = asyncio.Event()

    async def asyncTearDown(self):
        await self.jobs.close()
        self.tmp.cleanup()

    async def delayed(self, args):
        self.calls += 1
        self.started.set()
        await self.release.wait()
        return {"structuredContent": {"status":"submitted", "retry_safe":False}}

    async def test_slow_publish_is_durable_and_duplicate_retry_dispatches_once(self):
        job = await self.jobs.submit("owner", "publish_with_video", {"video":"test.mp4"}, self.delayed)
        self.assertEqual(job["status"], "queued")
        await self.started.wait()
        retry = await self.jobs.submit("owner", "publish_with_video", {"video":"test.mp4", "ai_generated":True}, self.delayed)
        self.assertEqual(retry["publication_id"], job["publication_id"])
        self.release.set()
        done = await self.jobs.wait("owner", job["publication_id"], 1)
        self.assertEqual(done["status"], "submitted")
        self.assertEqual(self.calls, 1)
        self.assertFalse(done["retry_safe"])

    async def test_disconnect_of_status_waiter_does_not_cancel_publication(self):
        job = await self.jobs.submit("owner", "publish_content", {}, self.delayed)
        await self.started.wait()
        waiter = asyncio.create_task(self.jobs.wait("owner", job["publication_id"], 15))
        waiter.cancel()
        await asyncio.gather(waiter, return_exceptions=True)
        self.release.set()
        self.assertEqual((await self.jobs.wait("owner",job["publication_id"],1))["status"], "submitted")

    async def test_transport_timeout_or_unstructured_reply_is_unknown_and_never_retried(self):
        async def timeout(args):
            self.calls += 1
            raise TimeoutError()
        job = await self.jobs.submit("owner","publish_content",{},timeout)
        done = await self.jobs.wait("owner",job["publication_id"],1)
        self.assertEqual(done["status"], "unknown")
        same = await self.jobs.submit("owner","publish_content",{},timeout)
        self.assertEqual(same["publication_id"],job["publication_id"])
        self.assertEqual(self.calls,1)
        async def unstructured(args): return {"isError":True}
        job = await self.jobs.submit("owner","publish_content",{"title":"different"},unstructured)
        self.assertEqual((await self.jobs.wait("owner",job["publication_id"],1))["status"],"unknown")

    async def test_idempotency_conflict_owner_isolation_and_explicit_new_intent(self):
        args = {"idempotency_key":"intent-1"}
        first = await self.jobs.submit("owner","publish_content",args,self.delayed)
        with self.assertRaises(ValueError):
            await self.jobs.submit("owner","publish_content",dict(args,title="changed"),self.delayed)
        with self.assertRaises(ValueError): self.jobs.get("other",first["publication_id"])
        self.assertEqual(self.jobs.recent("other"),[])
        self.release.set()
        await self.jobs.wait("owner",first["publication_id"],1)
        second = await self.jobs.submit("owner","publish_content",{"idempotency_key":"intent-2"},self.delayed)
        self.assertNotEqual(first["publication_id"],second["publication_id"])
        self.release.set()
        await self.jobs.wait("owner",second["publication_id"],1)
        self.assertEqual(self.calls,2)

    async def test_new_or_changed_key_cannot_bypass_inflight_or_unknown_protection(self):
        for old_key in (None,"original"):
            args = {"title":str(old_key)}
            if old_key: args["idempotency_key"] = old_key
            first = await self.jobs.submit("owner","publish_content",args,self.delayed)
            retry = await self.jobs.submit("owner","publish_content",dict(args,idempotency_key="new-"+str(old_key)),self.delayed)
            self.assertEqual(first["publication_id"],retry["publication_id"])
            self.jobs._update(first["publication_id"],"unknown")
            retry = await self.jobs.submit("owner","publish_content",dict(args,idempotency_key="newer-"+str(old_key)),self.delayed)
            self.assertEqual(first["publication_id"],retry["publication_id"])

    async def test_pre_submit_failure_can_be_corrected_but_same_key_is_stable(self):
        async def failed(args): return {"isError":True,"structuredContent":{"status":"failed","phase":"pre_submit","retry_safe":True}}
        first = await self.jobs.submit("owner","publish_content",{"idempotency_key":"bad"},failed)
        self.assertTrue((await self.jobs.wait("owner",first["publication_id"],1))["retry_safe"])
        retry = await self.jobs.submit("owner","publish_content",{"idempotency_key":"bad"},failed)
        self.assertEqual(first["publication_id"],retry["publication_id"])

    async def test_shutdown_and_restart_do_not_replay_in_flight_jobs(self):
        first = await self.jobs.submit("owner","publish_content",{},self.delayed)
        await self.started.wait()
        with self.assertRaises(RuntimeError): PublishJobs(self.path)
        await self.jobs.close()
        self.jobs = PublishJobs(self.path)
        self.assertEqual(self.jobs.get("owner",first["publication_id"])["status"],"unknown")
        retry = await self.jobs.submit("owner","publish_content",{},self.delayed)
        self.assertEqual(retry["publication_id"],first["publication_id"])
        self.assertEqual(self.calls,1)

    async def test_recovery_after_abrupt_process_loss_and_database_permissions(self):
        self.jobs.db.execute("INSERT INTO jobs VALUES ('interrupted','owner','publish_content','f',NULL,'running',1,1,NULL)")
        self.jobs.db.commit()
        await self.jobs.close()
        # Simulate the durable running row left by SIGKILL rather than clean shutdown.
        import sqlite3
        with sqlite3.connect(self.path) as db: db.execute("UPDATE jobs SET status='running'")
        self.jobs = PublishJobs(self.path)
        self.assertEqual(self.jobs.get("owner","interrupted")["status"],"unknown")
        self.assertEqual(self.path.stat().st_mode & 0o777,0o600)

    async def test_pending_limit_and_one_active_dispatch(self):
        self.jobs.max_pending = 2
        await self.jobs.submit("owner","publish_content",{"title":"1"},self.delayed)
        await self.started.wait()
        await self.jobs.submit("owner","publish_content",{"title":"2"},self.delayed)
        await asyncio.sleep(.02)
        self.assertEqual(self.calls,1)
        with self.assertRaises(ValueError):
            await self.jobs.submit("owner","publish_content",{"title":"3"},self.delayed)

if __name__ == "__main__": unittest.main()
