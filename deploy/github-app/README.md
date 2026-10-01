# The `noctra-agent` GitHub App

Noctra acts on GitHub as `noctra-agent[bot]`, a public GitHub App owned by the `onelastcommit` organisation. Users install the app on the repositories Noctra may touch, then run `noctra github login`. The app's private key never leaves the token service at `https://auth.getnoctra.dev`, which mints short-lived, single-repository installation tokens for linked Noctra instances.

`manifest.json` in this directory is the source of truth for the app's settings. Self-hosters can use it to create their own copy of the app (change `name`, the URLs and the owner).

## Settings at a glance

| Setting | Value |
|---|---|
| Name | `noctra-agent` (`noctra` is reserved by an existing account) |
| Owner | `onelastcommit` (organisation) |
| Homepage URL | `https://getnoctra.dev` |
| Redirect URI (older UI: Callback URL) | `https://auth.getnoctra.dev/installed` |
| Expire user authorization tokens | On |
| Request user authorization (OAuth) during installation | On |
| Enable Device Flow | On |
| Setup URL | (empty, GitHub disables it when OAuth-on-install is on) |
| Webhook | Active, `https://auth.getnoctra.dev/webhook`, with a secret |
| Repository permissions | Contents: Read and write; Pull requests: Read and write; Issues: Read and write; Checks: Read-only; Actions: Read-only; Metadata: Read-only (mandatory) |
| Workflows permission | **No access** |
| Organisation and account permissions | None |
| Subscribed events | None ticked (see below) |
| Where can this app be installed | Any account |

`installation`, `installation_repositories` and `github_app_authorization` are delivered to every GitHub App automatically and do not appear in the event list, so no event boxes need ticking. Those three are the only events the token service handles.

No client secret is needed: the device flow uses only the public client ID, and the token service authenticates as the app with its private key.

## Creating the app

The app can be created by hand in the web UI (recommended for the hosted app, because the token service does not exist yet) or through GitHub's manifest flow once the token service is deployed. Do not install the app anywhere until the token service is live, otherwise the `installation` webhook is lost.

### 1. Generate the webhook secret

On your own machine, without echoing it to the terminal:

```bash
umask 077
openssl rand -hex 32 > ~/noctra-webhook-secret.txt
```

Open the file in an editor when you need to paste the value. You will paste it into GitHub (step 2) and into the Worker's secrets (Phase 2).

### 2. Create the app

1. Go to `https://github.com/organizations/onelastcommit/settings/apps/new` (you must be an organisation owner).
2. **GitHub App name**: `noctra-agent`. App names are unique across all of GitHub and cannot match an existing account, which rules out `noctra`. The slug is used by the watcher (`app/noctra-agent`) and in the bot's commit identity, so a self-hoster using a different name must set it in their token service config.
3. **Description**: copy `description` from `manifest.json`.
4. **Homepage URL**: `https://getnoctra.dev`.
5. **Identifying and authorizing users**:
   - **Redirect URI** (labelled *Callback URL* in older versions of the page): `https://auth.getnoctra.dev/installed`, with **Allow wildcard matching** unticked
   - **Expire user authorization tokens**: ticked
   - **Request user authorization (OAuth) during installation**: ticked
   - **Enable Device Flow**: ticked
6. **Post installation**: leave the Setup URL empty.
7. **Webhook**:
   - **Active**: ticked
   - **Webhook URL**: `https://auth.getnoctra.dev/webhook`
   - **Webhook secret**: paste the contents of `~/noctra-webhook-secret.txt`
   - SSL verification: enabled
8. **Permissions**, under *Repository permissions*:
   - Actions: Read-only
   - Checks: Read-only
   - Contents: Read and write
   - Issues: Read and write
   - Metadata: Read-only (forced)
   - Pull requests: Read and write
   - Everything else, **including Workflows**: No access
   - *Organisation permissions* and *Account permissions*: leave all as No access
9. **Subscribe to events**: leave every box unticked.
10. **Where can this GitHub App be installed?**: *Any account*.
11. Click **Create GitHub App**.

### 3. Record the public identifiers

On the app's *General* page note the **App ID** and the **Client ID**. Neither is secret; both are needed for the token service configuration in Phase 2. Do not click *Generate a new client secret*.

### 4. Generate and convert the private key

1. On the app's *General* page, under *Private keys*, click **Generate a private key**. GitHub downloads `noctra-agent.<date>.private-key.pem`.
2. Convert it to PKCS#8, which is the format the Workers runtime's WebCrypto can import:

   ```bash
   umask 077
   openssl pkcs8 -topk8 -nocrypt \
     -in ~/Downloads/noctra-agent.*.private-key.pem \
     -out ~/noctra-app-key.pkcs8.pem
   ```

3. Store the original `.pem` in your password manager as the recovery copy. Keep `~/noctra-app-key.pkcs8.pem` only until it has been loaded into the Worker in Phase 2, then delete it with `shred -u` (or `rm -P` on macOS), along with the downloaded original and `~/noctra-webhook-secret.txt`.

Never commit either file, paste it into an issue or chat, or copy it to the Pi. If a key is ever exposed, generate a new one on the same page, load it into the Worker, then delete the old key in GitHub; tokens minted with the old key stop working within an hour.

### 5. Optional: add a logo

*General* page, *Display information*. Purely cosmetic.

## The bot's commit identity

Commits are authored as `noctra-agent[bot]` using GitHub's no-reply address, which needs the bot user's numeric ID (different from the App ID). Once the app exists:

```bash
gh api "users/noctra-agent[bot]" --jq .id
```

The commit identity is then:

```
noctra-agent[bot] <ID+noctra-agent[bot]@users.noreply.github.com>
```

The token service publishes this ID from `GET /config`, so Noctra clients pick it up without configuration.

## Using the manifest flow (self-hosters)

GitHub can create an app from `manifest.json` in one step: a form POSTs the manifest to `https://github.com/settings/apps/new` (or `/organizations/<org>/settings/apps/new`), GitHub redirects to `redirect_url` with a one-time `code`, and the code is exchanged for the app's credentials. The token service will serve that flow at `/manifest-callback` so a self-hoster's deployment can create its own app and store the returned private key and webhook secret straight into its secrets. Device flow is not a manifest field, so tick **Enable Device Flow** on the new app's *General* page afterwards.
