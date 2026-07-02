def report(id, config):
    """Write a system info report file using facts and builtins."""
    path = config.get("path", "/tmp/sysinfo-report.txt")

    # Gather info using builtins.
    hostname_result = cmd_run("hostname")
    hostname = hostname_result["stdout"].strip()

    uptime_result = cmd_run("uptime", args=["-s"])
    uptime = uptime_result["stdout"].strip()

    lines = [
        "=== Zester Starlark Module Report ===",
        "hostname: %s" % hostname,
        "uptime_since: %s" % uptime,
        "os_family: %s" % facts.get("os", {}).get("family", "unknown"),
        "arch: %s" % facts.get("os", {}).get("arch", "unknown"),
    ]

    # Check if curl is available.
    if file_exists("/usr/bin/curl"):
        lines.append("curl: available")
    else:
        lines.append("curl: not found")

    content = "\n".join(lines) + "\n"
    file_write(path, content, 0o644)

    log.info("sysinfo.report: wrote report to %s" % path)
    return {"changed": True, "diff": "wrote report to %s" % path, "details": {"hostname": hostname}}

def report_check(id, config):
    """Check if report already exists."""
    path = config.get("path", "/tmp/sysinfo-report.txt")
    if file_exists(path):
        return {"needs_change": False}
    return {"needs_change": True, "diff": "report not yet written"}

def report_revert(id, config):
    """Remove the report file."""
    path = config.get("path", "/tmp/sysinfo-report.txt")
    if file_exists(path):
        file_remove(path)
        return {"changed": True, "diff": "removed %s" % path}
    return {"changed": False}

def hash_check(id, config):
    """Verify a file's SHA256 hash."""
    path = config["path"]
    expected = config["expected_hash"]

    if not file_exists(path):
        return {"changed": True, "diff": "file does not exist: %s" % path}

    content = file_read(path)
    actual = hash_sha256(content)

    if actual == expected:
        return {"changed": False, "details": {"hash": actual}}

    return {
        "changed": True,
        "diff": "hash mismatch for %s: expected %s, got %s" % (path, expected, actual),
        "details": {"expected": expected, "actual": actual},
    }
