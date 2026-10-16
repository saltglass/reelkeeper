# Signing in and security

Everything about who can use Reelkeeper is on one page: Settings → Security.
This page explains each option and the order to set them up in.

Reelkeeper has no user roles. Anyone who can sign in can do everything,
including changing settings. Treat every account, and the API key, as
administrator access.

## Local accounts

The first visit creates the first account (see
[First-time setup](first-run.md)). Passwords must be at least 8 characters and
are stored as argon2id hashes. Signing in sets a browser cookie, and the
session lasts 30 days by default. Change that under Show advanced → "Stay
signed in for (days)" on the Security page.

To change your password, open the account menu at the top of the page and
choose Change password. Repeated wrong passwords from the same address or for
the same username are slowed down for a while.

Settings → Security → "Allow signing in with a username and password" is on by
default.

## Single sign-on (OIDC), with Authentik as the example

If you run an identity provider such as Authentik, Reelkeeper can sign people
in through it using OpenID Connect (with PKCE). The example below uses
Authentik; other providers follow the same pattern.

You need two things in place first:

1. Reelkeeper reachable over HTTPS on its own name, for example
   `https://reelkeeper.example.com`, through a reverse proxy.
2. That proxy listed under Reverse proxies on the Security page (see
   [below](#reverse-proxies-and-the-url-base)). Reelkeeper builds the redirect
   address from the address your browser used, and can only trust it when it
   knows the proxy.

**In Reelkeeper:** open Settings → Security, switch on "Use single sign-on",
and copy the **Redirect URI** that appears. It looks like
`https://reelkeeper.example.com/api/v1/auth/oidc/callback`.

**In Authentik:**

1. Create an OAuth2/OpenID provider. Use a confidential client, and paste the
   Redirect URI above as an allowed (strict) redirect. Leave the default
   scopes (`openid`, `profile`, `email`).
2. Create an application that uses the provider, and note its slug.
3. Bind the application to the group (or users) who may use Reelkeeper, so
   other Authentik users cannot open it.
4. Copy the provider's Client ID and Client Secret.

**Back in Reelkeeper**, fill in:

- **Issuer URL**: `https://auth.example.com/application/o/<application-slug>/`
  (with the trailing slash). Press **Check**; it fetches the provider's
  details and reports whether they look right.
- **Client ID** and **Client secret** from Authentik. The secret is stored
  sealed and never shown again.
- **Required group**: the name of an Authentik group. Only members can sign
  in. **Set this.** If you leave it empty, everyone your provider lets
  through gets full access, and a new account is created for them
  automatically the first time they sign in.

Save. The login page now shows a "Sign in with SSO" button (the text can be
changed under Show advanced → "Sign-in button text"). Under Show advanced you
can also change the **scopes** and the **groups claim** (the token field that
lists a user's groups; Authentik uses `groups`, which is the default).

### Linking single sign-on to your account

The first time someone signs in through the provider, Reelkeeper makes a
**new** account for them. It never merges accounts by matching names, so a
provider account called "dan" does not take over a local account called "dan".

To use single sign-on with the account you already have, sign in with your
password first, open the account menu and choose **Link single sign-on**. After
linking, both ways reach the same account. This is how you avoid ending up with
two accounts for one person.

### Turning password sign-in off

Once single sign-on works, you can switch off "Allow signing in with a username
and password" so only the provider can be used. Reelkeeper will not let you
until single sign-on has been saved and used successfully once with the current
issuer and client ID, so you cannot lock yourself out by a typo. The page shows
the time of the last successful sign-in.

If the provider ever breaks, see [Getting back in](#getting-back-in).

## The API key

Seerr, dashboards and scripts do not sign in; they send an API key. It is on
the same page, in the **API key** card: press the eye to show it and the copy
button to copy it. Reelkeeper sends it either as an `X-Api-Key` header or as an
`apikey` parameter in the address. It works on `/api` and `/compat` (the
Radarr/Sonarr-style addresses for [Seerr](seerr.md)).

Treat it like a password. **Generate new key** replaces it; the old key stops
working at once, so update everything that used it. A wrong key is always
refused, even from the bypass list.

## The bypass list

Settings → Security → Access without signing in → **Trusted networks**. Anyone
connecting from an address in this list uses Reelkeeper without signing in.
Enter one IP address or range per line, for example `192.168.1.0/24`. It is
empty by default, which means sign-in is always required, and that is the
right choice unless you have a reason.

**Warning if you use a reverse proxy.** When Reelkeeper sits behind a reverse
proxy, every request reaches Reelkeeper from the proxy's address. If you put
the proxy's address, or the whole network the proxy lives on, in the trusted
networks, then **everyone coming through the proxy skips sign-in**, including
people on the internet. Never do that. Use the "Reverse proxies" box described
below instead, so Reelkeeper can see the real visitor address, and only list in
the trusted networks addresses of real devices, such as your home network.

Remember, too, that when Reelkeeper is reachable from the internet, only its
own sign-in stands between strangers and your library. Keep the list empty
unless you are sure.

## Reverse proxies and the URL base

**Reverse proxies** (same page): list the address of each reverse proxy in
front of Reelkeeper, one per line. Reelkeeper only believes the
`X-Forwarded-For`, `X-Forwarded-Proto` and `X-Forwarded-Host` headers that
those addresses send. This matters for three things:

- the real visitor address (for the bypass list and the log),
- the Redirect URI shown for single sign-on, and
- whether the sign-in cookie is marked secure (HTTPS only).

If the headers were believed from anywhere, anyone could pretend to be a
trusted address. If you do not use a proxy, leave the box empty.

Make sure your proxy actually sends the `X-Forwarded-For`,
`X-Forwarded-Proto` and `X-Forwarded-Host` headers. Most do by default.

**URL base:** if you serve Reelkeeper below a path such as
`https://home.example.com/reelkeeper` instead of on its own hostname, set
`REELKEEPER_URL_BASE=/reelkeeper` (see [Installing](install.md)). Reelkeeper's
pages, the single sign-on return address and the Seerr addresses all follow it.
A hostname of its own is simpler and usually works better.

## Getting back in

Lost your password, or single sign-on is down and you turned password sign-in
off? Run this on the server that runs Reelkeeper:

```sh
docker exec reelkeeper /reelkeeper reset-auth
```

It turns password sign-in back on, gives the first account a new random
password and prints it once, and ends every sign-in. (If no account exists, it
creates one called `admin`.) Sign in with the printed password, then change it
from the account menu. Running it inside the container means it uses the same
database and secret key as the server.

The login page also says to run this command when no sign-in method is turned
on at all.

Another way: start Reelkeeper once with the environment variable
`REELKEEPER_RESET_AUTH=true`. This only turns password sign-in back on, without
changing any password. **Remove the variable afterwards**, because it runs on
every start while it is set.
