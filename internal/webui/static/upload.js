// Fingerprints a file, declares it, delivers the bytes directly to the Evidence
// Vault (ADR-0002) and completes the upload. This is the UI's only script
// (ADR-0003), and it holds no rules: the service and the vault decide.
(() => {
  const form = document.getElementById("upload");
  const status = document.getElementById("upload-status");
  if (!form || !status) return;
  const submit = form.querySelector('button[type="submit"]');

  // Thrown with text a person can act on. offerRetry is the completion to retry.
  class Stop extends Error {
    constructor(message, offerRetry) {
      super(message);
      this.offerRetry = offerRetry;
    }
  }

  function show(message, kind, retryUrl) {
    status.replaceChildren(document.createTextNode(message));
    status.className = "status status-" + kind;
    status.hidden = false;
    if (retryUrl) {
      const again = document.createElement("button");
      again.type = "button";
      again.textContent = "Retry completion";
      again.dataset.completeUrl = retryUrl;
      status.append(" ", again);
    }
  }

  // Calls the service. A session that has gone sends the browser to sign-in.
  async function call(url, options) {
    let res;
    try {
      res = await fetch(url, { method: "POST", ...options });
    } catch {
      return { ok: false, unreachable: true };
    }
    if (res.redirected) {
      location.assign(res.url);
      return new Promise(() => {});
    }
    if (res.ok) {
      return { ok: true, body: res.status === 204 ? null : await res.json() };
    }
    const failure = await res.json().catch(() => ({ message: "The service couldn't finish this step.", retry: true }));
    return { ok: false, ...failure };
  }

  async function fingerprint(file) {
    const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
    return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
  }

  // Only the vault's status is read: its response body is S3 XML, for no one here.
  async function deliver(grant, file) {
    const headers = Object.fromEntries(
      Object.entries(grant.headers).filter(([name]) => name.toLowerCase() !== "content-length"),
    );
    let res;
    try {
      res = await fetch(grant.url, { method: grant.method, headers, body: file });
    } catch {
      throw new Stop("The Evidence Vault couldn't be reached from this address, so the file wasn't stored. Check that you opened the UI at its configured address, then start a new upload.");
    }
    if (!res.ok) {
      throw new Stop(`The Evidence Vault refused the file (status ${res.status}), so it wasn't stored. Start a new upload.`);
    }
  }

  async function complete(url) {
    show("Completing the upload…", "info");
    const result = await call(url, {});
    if (result.ok) {
      location.reload();
      return new Promise(() => {});
    }
    if (result.unreachable) {
      throw new Stop("The file reached the Evidence Vault, but the service couldn't be reached to complete the upload.", url);
    }
    throw new Stop(result.message, result.retry ? url : undefined);
  }

  async function upload() {
    const file = form.elements.file.files[0];
    if (file.size === 0 || file.size > Number(form.dataset.maxBytes)) {
      throw new Stop("The file must be no larger than 10 MiB, and not empty.");
    }
    show("Fingerprinting the file…", "info");
    const sha256 = await fingerprint(file);

    show("Declaring the Document…", "info");
    const declared = await call(form.getAttribute("action"), {
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        doc_type: form.elements.doc_type.value,
        expiry_date: form.elements.expiry_date.value,
        content_type: file.type,
        size_bytes: file.size,
        sha256,
      }),
    });
    if (declared.unreachable) throw new Stop("The service couldn't be reached. Nothing was uploaded. Try again.");
    if (!declared.ok) throw new Stop(declared.message);

    show("Uploading to the Evidence Vault…", "info");
    await deliver(declared.body, file);
    await complete(declared.body.complete_url);
  }

  function report(err) {
    if (err instanceof Stop) {
      show(err.message, "error", err.offerRetry);
      return;
    }
    console.error(err);
    show("The upload stopped unexpectedly. Start a new upload.", "error");
  }

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    try {
      await upload();
    } catch (err) {
      report(err);
    } finally {
      submit.disabled = false;
    }
  });

  // Retries appear in the Documents table and in the status message.
  document.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-complete-url]");
    if (!button) return;
    button.disabled = true;
    try {
      await complete(button.dataset.completeUrl);
    } catch (err) {
      report(err);
    } finally {
      button.disabled = false;
    }
  });
})();
