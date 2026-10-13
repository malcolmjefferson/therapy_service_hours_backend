"""Dev-only IAM verification. Capture all identity data; print only safe counts/codes."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


class VerificationFailure(Exception):
    """Only static, non-sensitive codes are exposed."""


def aws_call(service, operation, payload):
    with tempfile.TemporaryDirectory(prefix="dev-binding-check-") as directory:
        root = Path(directory)
        if root.resolve().parent != Path(tempfile.gettempdir()).resolve():
            raise VerificationFailure("INVALID_TEMP_DIRECTORY")
        root.chmod(0o700)
        request = root / "input.json"
        request.write_text(json.dumps(payload), encoding="utf-8")
        request.chmod(0o600)
        args = ["aws", service, operation, "--region", "us-west-2", "--no-cli-pager", "--no-cli-auto-prompt", "--output", "json"]
        if service == "lambda":
            args += ["--function-name", "therapy-service-hours-dev-seed", "--payload", "fileb://" + str(request), str(root / "response.json")]
        else:
            args += ["--cli-input-json", "file://" + str(request)]
        result = subprocess.run(args, capture_output=True, text=True, encoding="utf-8", timeout=60, check=False)
        if result.returncode:
            raise VerificationFailure("VERIFICATION_AWS_CALL_FAILED")
        response = json.loads(result.stdout or "{}")
        if service == "lambda":
            if response.get("FunctionError"):
                raise VerificationFailure("VERIFICATION_LAMBDA_FAILED")
            response = json.loads((root / "response.json").read_text(encoding="utf-8"))
        return response


def verify(env, aws):
    account = env.get("EXPECTED_AWS_ACCOUNT", "")
    pool = env.get("USER_POOL_ID", "")
    email = env.get("ADMIN_EMAIL", "").strip().lower()
    if env.get("APP_ENV") != "dev" or not re.fullmatch(r"\d{12}", account) or not re.fullmatch(r"us-west-2_[A-Za-z0-9]+", pool):
        raise VerificationFailure("DEV_VERIFICATION_ONLY")
    if not email or any(char in email for char in ('"', '\\', '\n', '\r')):
        raise VerificationFailure("INVALID_VERIFICATION_EMAIL")
    if aws("sts", "get-caller-identity", {}).get("Account") != account:
        raise VerificationFailure("VERIFICATION_ACCOUNT_MISMATCH")
    if aws("cognito-idp", "describe-user-pool", {"UserPoolId": pool})["UserPool"].get("Name") != "therapy-service-hours-dev-users":
        raise VerificationFailure("VERIFICATION_POOL_MISMATCH")
    response = aws("cognito-idp", "list-users", {"UserPoolId": pool, "Filter": 'email = "' + email + '"', "Limit": 60})
    users = response.get("Users", [])
    if response.get("PaginationToken") or len(users) != 1:
        raise VerificationFailure("VERIFICATION_IDENTITY_COUNT_MISMATCH")
    user = users[0]
    attributes = {item["Name"]: item["Value"] for item in user.get("Attributes", [])}
    if not user.get("Username", "").lower().startswith("google_") or user.get("Enabled") is not True or user.get("UserStatus") != "EXTERNAL_PROVIDER" or attributes.get("email") != email or attributes.get("email_verified") != "true" or not attributes.get("sub"):
        raise VerificationFailure("VERIFICATION_IDENTITY_MISMATCH")
    # Omit admin_email: an older seed binary then refuses before storage instead
    # of silently treating an unknown operation as a seed request.
    result = aws("lambda", "invoke", {"operation": "verify-binding", "verification_email": email, "expected_subject": attributes["sub"]})
    binding = result.get("binding", {})
    expected = ("users", "subject_users", "matching_active_admin", "accepted_invitations")
    if result.get("status") != "ok" or set(binding) != set(expected) or any(type(binding[key]) is not int or binding[key] != 1 for key in expected):
        raise VerificationFailure("VERIFICATION_BINDING_MISMATCH")
    return {"status": "ok", "google_identities": 1, "binding": {key: binding[key] for key in expected}}


def main():
    try:
        print(json.dumps(verify(dict(os.environ), aws_call)))
    except VerificationFailure as error:
        print(json.dumps({"status": "error", "code": str(error)}))
        return 1
    except Exception:
        # CLI/JSON exceptions can embed emails and subjects. Never print them.
        print(json.dumps({"status": "error", "code": "VERIFICATION_FAILED"}))
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
