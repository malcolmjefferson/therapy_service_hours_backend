import contextlib
import io
import json
import os
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import verify_dev_binding as verification


class BindingVerificationTests(unittest.TestCase):
    env = {"APP_ENV": "dev", "EXPECTED_AWS_ACCOUNT": "123456789012", "USER_POOL_ID": "us-west-2_Fictional", "ADMIN_EMAIL": "owner@example.test"}

    def aws(self, service, operation, payload):
        if service == "sts":
            return {"Account": self.env["EXPECTED_AWS_ACCOUNT"]}
        if operation == "describe-user-pool":
            return {"UserPool": {"Name": "therapy-service-hours-dev-users"}}
        if operation == "list-users":
            return {"Users": [{"Username": "google_fictional", "Enabled": True, "UserStatus": "EXTERNAL_PROVIDER", "Attributes": [{"Name": "email", "Value": self.env["ADMIN_EMAIL"]}, {"Name": "email_verified", "Value": "true"}, {"Name": "sub", "Value": "private-subject"}]}]}
        self.assertEqual(payload, {"operation": "verify-binding", "verification_email": self.env["ADMIN_EMAIL"], "expected_subject": "private-subject"})
        return {"status": "ok", "binding": dict.fromkeys(("users", "subject_users", "matching_active_admin", "accepted_invitations"), 1)}

    def test_safe_counts_and_old_binary_safe_request(self):
        result = verification.verify(self.env, self.aws)
        self.assertEqual(result["binding"]["accepted_invitations"], 1)
        self.assertNotIn("private-subject", json.dumps(result))
        self.assertNotIn(self.env["ADMIN_EMAIL"], json.dumps(result))

    def test_guards_refuse_before_aws(self):
        for changed in ({"APP_ENV": "prod"}, {"EXPECTED_AWS_ACCOUNT": ""}, {"USER_POOL_ID": "us-east-1_Fictional"}, {"ADMIN_EMAIL": 'unsafe"@example.test'}):
            with patch.object(verification, "aws_call") as aws:
                with self.assertRaises(verification.VerificationFailure):
                    verification.verify({**self.env, **changed}, aws)
                aws.assert_not_called()

    def test_wrong_account_pool_identity_and_counts_refused(self):
        for bad_operation in ("get-caller-identity", "describe-user-pool", "list-users", "invoke"):
            def aws(service, operation, payload):
                if operation == bad_operation:
                    return {"Account": "000000000000", "UserPool": {"Name": "foreign"}, "Users": [], "status": "ok", "binding": {}}
                return self.aws(service, operation, payload)
            with self.assertRaises(verification.VerificationFailure):
                verification.verify(self.env, aws)

    def test_cli_keeps_inputs_private_and_cleans_files(self):
        seen = []
        def run(args, **kwargs):
            self.assertTrue(kwargs["capture_output"])
            self.assertNotIn("private-subject", " ".join(args))
            request = Path(next(value[8:] for value in args if value.startswith("fileb://")))
            seen.append(request.parent)
            self.assertIn("private-subject", request.read_text())
            Path(args[-1]).write_text('{"status":"ok"}')
            return SimpleNamespace(returncode=0, stdout="{}")
        with patch.object(verification.subprocess, "run", side_effect=run):
            self.assertEqual(verification.aws_call("lambda", "invoke", {"expected_subject": "private-subject"}), {"status": "ok"})
        self.assertTrue(all(not path.exists() for path in seen))

    def test_unverified_disabled_or_ambiguous_identity_never_invokes_lambda(self):
        original = self.aws("cognito-idp", "list-users", {})
        for response in (
            {"Users": original["Users"] * 2},
            {**original, "PaginationToken": "more"},
            {"Users": [{**original["Users"][0], "Enabled": False}]},
            {"Users": [{**original["Users"][0], "Attributes": [{"Name": "email", "Value": self.env["ADMIN_EMAIL"]}, {"Name": "email_verified", "Value": "false"}, {"Name": "sub", "Value": "private-subject"}]}]},
        ):
            def aws(service, operation, payload):
                self.assertNotEqual(service, "lambda")
                return response if operation == "list-users" else self.aws(service, operation, payload)
            with self.assertRaises(verification.VerificationFailure):
                verification.verify(self.env, aws)

    def test_cli_failure_removes_private_inputs_and_redacts_error(self):
        seen = []
        def run(args, **kwargs):
            request = Path(next(value[8:] for value in args if value.startswith("fileb://")))
            seen.append(request.parent)
            return SimpleNamespace(returncode=1, stdout="private-subject", stderr="owner@example.test")
        with patch.object(verification.subprocess, "run", side_effect=run):
            with self.assertRaisesRegex(verification.VerificationFailure, "^VERIFICATION_AWS_CALL_FAILED$"):
                verification.aws_call("lambda", "invoke", {"expected_subject": "private-subject"})
        self.assertTrue(all(not path.exists() for path in seen))

    def test_raw_errors_are_not_printed(self):
        output = io.StringIO()
        with patch.dict(os.environ, self.env), patch.object(verification, "aws_call", side_effect=Exception("private-subject owner@example.test")), contextlib.redirect_stdout(output):
            self.assertEqual(verification.main(), 1)
        self.assertEqual(json.loads(output.getvalue()), {"status": "error", "code": "VERIFICATION_FAILED"})


if __name__ == "__main__":
    unittest.main()
