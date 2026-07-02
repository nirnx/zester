def hello(id, config):
    """A formula-specific module that only exists inside dynamic-test/_modules/."""
    name = config.get("name", "world")
    msg = "Hello, %s! From a dynamically loaded Starlark module." % name
    file_write("/tmp/greeter-output.txt", msg + "\n", 0o644)
    log.info("greeter.hello: wrote greeting for %s" % name)
    return {"changed": True, "diff": "greeted %s" % name, "details": {"name": name}}

def hello_check(id, config):
    if file_exists("/tmp/greeter-output.txt"):
        return {"needs_change": False}
    return {"needs_change": True, "diff": "greeting not yet written"}
