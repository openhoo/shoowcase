import { useEffect, type ReactNode } from "react";
import {
  ArrowLeft,
  ArrowRight,
  CheckCircle2,
  ExternalLink,
  FileUp,
  Globe2,
  KeyRound,
  Layers3,
  LockKeyhole,
  ShieldCheck,
  TriangleAlert,
} from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type PasswordPage = {
  screen: "password";
  Register: boolean;
  Open: boolean;
  Flow: string;
  Message: string;
  OIDC: string;
  Signup: string;
  Back: string;
};
type ApprovalPage = {
  screen: "approve";
  Site: string;
  Revision: string;
  Preview: string;
  Tool: string;
  CSRF: string;
};
type UploadPage = {
  screen: "upload";
  Site: string;
  Path: string;
  Size: number;
  SHA: string;
  CSRF: string;
};
type ReceiptPage = {
  screen: "receipt";
  Title: string;
  Message: string;
  Site: string;
  Revision: string;
  Preview?: string;
  SiteURL?: string;
};
type PublicPage =
  | PasswordPage
  | ApprovalPage
  | UploadPage
  | ReceiptPage
  | { screen: "landing"; Signup?: string }
  | { screen: "continue"; Next: string }
  | { screen: "separate-sign-in" }
  | { screen: "error"; Title: string; Message: string };

const link =
  "inline-flex items-center gap-2 text-primary underline underline-offset-4";
const sourceValue = "break-all font-mono text-sm";
const signIn = "/auth/login?next=/sites";

function Shell({
  children,
  landing = false,
}: {
  children: ReactNode;
  landing?: boolean;
}) {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <a
        href="#content"
        className="sr-only z-50 rounded-md bg-background px-4 py-2 font-medium text-foreground focus:not-sr-only focus:absolute focus:left-3 focus:top-3"
      >
        Skip to content
      </a>
      <header className="border-b bg-card">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-3 px-4 py-3 sm:px-8">
          <a
            href="/"
            className="inline-flex items-center gap-2.5 font-semibold tracking-tight"
          >
            <span className="inline-flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
              <Layers3 className="size-4" aria-hidden="true" />
            </span>
            Shoowcase
          </a>
          {landing && (
            <Button asChild variant="ghost" size="sm">
              <a href={signIn}>
                Sign in <ArrowRight aria-hidden="true" />
              </a>
            </Button>
          )}
        </div>
      </header>
      <main
        id="content"
        tabIndex={-1}
        className={
          landing
            ? "mx-auto max-w-6xl px-4 py-14 outline-none sm:px-8 sm:py-24"
            : "mx-auto grid max-w-2xl gap-6 px-4 py-9 outline-none sm:px-8 sm:py-14"
        }
      >
        {children}
      </main>
    </div>
  );
}

function Heading({
  tag,
  title,
  children,
}: {
  tag: string;
  title: string;
  children?: ReactNode;
}) {
  return (
    <header className="min-w-0 space-y-2">
      <p className="text-xs font-semibold uppercase tracking-widest text-muted-foreground">
        {tag}
      </p>
      <h1 className="break-words text-3xl font-semibold tracking-tight sm:text-4xl">
        {title}
      </h1>
      {children && (
        <p className="leading-relaxed text-muted-foreground">{children}</p>
      )}
    </header>
  );
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="divide-y rounded-lg border bg-card px-4 sm:px-5">
      {rows.map(([name, value]) => (
        <div
          key={name}
          className="grid min-w-0 gap-1 py-3 sm:grid-cols-[8rem_1fr] sm:gap-4"
        >
          <dt className="text-sm font-medium text-muted-foreground">{name}</dt>
          <dd className="min-w-0 break-words">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Landing({ signup }: { signup?: string }) {
  return (
    <Shell landing>
      <div className="grid items-center gap-12 lg:grid-cols-[1.1fr_.9fr]">
        <section className="min-w-0 space-y-6">
          <Badge variant="secondary">
            <Layers3 aria-hidden="true" />
            Team publishing
          </Badge>
          <h1 className="max-w-xl text-4xl font-semibold leading-tight tracking-tight sm:text-6xl">
            A home for your team’s sites.
          </h1>
          <p className="max-w-lg text-lg leading-relaxed text-muted-foreground">
            Upload a web app, write a page, or connect your agent. Preview
            changes privately, then share a link when you’re ready.
          </p>
          <div className="flex flex-wrap gap-3">
            {signup && (
              <Button asChild>
                <a href={signup}>
                  Create an account <ArrowRight aria-hidden="true" />
                </a>
              </Button>
            )}
            <Button asChild variant={signup ? "outline" : "default"}>
              <a href={signIn}>Sign in</a>
            </Button>
            <Button asChild variant="ghost">
              <a href="/quickstart">Connect an agent</a>
            </Button>
          </div>
        </section>
        <div
          aria-hidden="true"
          className="rounded-3xl border bg-muted p-5 sm:p-9"
        >
          <div className="space-y-5 rounded-2xl border bg-card p-5 shadow-sm sm:p-7">
            <div className="flex items-center justify-between gap-4 text-muted-foreground">
              <Layers3 className="size-5" />
              <span className="text-xs font-medium uppercase tracking-widest">
                Team site
              </span>
            </div>
            <div className="flex h-24 items-end rounded-xl bg-primary p-4 text-primary-foreground">
              <Globe2 className="size-10" />
            </div>
            <div className="space-y-2">
              <div className="h-2 w-3/5 rounded-full bg-foreground/70" />
              <div className="h-2 w-4/5 rounded-full bg-muted" />
              <div className="h-2 w-2/3 rounded-full bg-muted" />
            </div>
            <div className="flex items-center gap-2 border-t pt-4 text-sm text-muted-foreground">
              <LockKeyhole className="size-4" />
              Private preview
            </div>
          </div>
        </div>
      </div>
    </Shell>
  );
}

function Password({ p }: { p: PasswordPage }) {
  return (
    <Shell>
      <Heading
        tag="Account"
        title={p.Register ? "Create an account" : "Sign in"}
      />
      {p.Message && (
        <Alert variant="destructive" role="alert">
          <TriangleAlert aria-hidden="true" />
          <AlertTitle>
            {p.Register ? "Account not created" : "Could not sign in"}
          </AlertTitle>
          <AlertDescription>{p.Message}</AlertDescription>
        </Alert>
      )}
      {!p.Register && (
        <Card>
          <CardHeader>
            <CardTitle>
              <h2 className="flex items-center gap-2">
                <ShieldCheck className="size-5" aria-hidden="true" />
                Work account
              </h2>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Button asChild variant="outline">
              <a href={p.OIDC}>
                Continue with single sign-on <ArrowRight aria-hidden="true" />
              </a>
            </Button>
          </CardContent>
        </Card>
      )}
      <Card>
        <CardHeader>
          <CardTitle>
            <h2 className="flex items-center gap-2">
              <KeyRound className="size-5" aria-hidden="true" />
              {p.Register
                ? "Choose your sign-in details"
                : "Username and password"}
            </h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <form
            action={p.Register ? "/auth/register" : "/auth/local"}
            method="post"
            className="grid gap-5"
          >
            <input type="hidden" name="flow" value={p.Flow} />
            <div className="grid gap-2">
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                name="username"
                required
                maxLength={64}
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                aria-describedby={p.Register ? "username-help" : undefined}
              />
              {p.Register && (
                <small id="username-help" className="text-muted-foreground">
                  Use 3–64 letters, numbers, periods, underscores or hyphens.
                  Start with a letter or number.
                </small>
              )}
            </div>
            <div className="grid gap-2">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                name="password"
                type="password"
                required
                minLength={p.Register ? 12 : undefined}
                autoComplete={p.Register ? "new-password" : "current-password"}
                aria-describedby={p.Register ? "password-help" : undefined}
              />
              {p.Register && (
                <small id="password-help" className="text-muted-foreground">
                  Use at least 12 characters.
                </small>
              )}
            </div>
            <div>
              <Button type="submit">
                {p.Register ? "Create account" : "Sign in with password"}{" "}
                <ArrowRight aria-hidden="true" />
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
      {p.Register ? (
        <div>
          <a className={link} href={p.Back}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            Back to sign in
          </a>
        </div>
      ) : (
        p.Open && (
          <div>
            <a className={link} href={p.Signup}>
              Create a local account
            </a>
          </div>
        )
      )}
    </Shell>
  );
}

function Continue({ next }: { next: string }) {
  // A POST from the password form must finish on the control origin. The
  // ordinary link below also completes the handoff without JavaScript.
  useEffect(() => {
    if (next.startsWith("/auth/login?ticket=")) window.location.replace(next);
  }, [next]);
  return (
    <Shell>
      <Heading tag="Account" title="Signed in">
        Opening your site.
      </Heading>
      <div>
        <Button asChild>
          <a href={next}>
            Continue to your site <ArrowRight aria-hidden="true" />
          </a>
        </Button>
      </div>
    </Shell>
  );
}

function Approval({ p }: { p: ApprovalPage }) {
  return (
    <Shell>
      <Heading tag="Publishing" title="Review public release">
        Approval makes this version available to anyone who knows the site
        address. The current version remains unchanged until you approve.
      </Heading>
      <Facts
        rows={[
          ["Site", p.Site],
          ["Version", <code className={sourceValue}>{p.Revision}</code>],
          ["Action", p.Tool],
        ]}
      />
      {p.Preview && (
        <div>
          <a
            href={p.Preview}
            className={link}
            target="_blank"
            rel="noopener noreferrer"
          >
            Review this version{" "}
            <ExternalLink className="size-4" aria-hidden="true" />
          </a>
        </div>
      )}
      <form method="post">
        <input type="hidden" name="csrf" value={p.CSRF} />
        <Button type="submit">
          <Globe2 aria-hidden="true" />
          Approve public release
        </Button>
      </form>
    </Shell>
  );
}

function Upload({ p }: { p: UploadPage }) {
  return (
    <Shell>
      <Heading tag="Draft files" title="Upload a file to the draft">
        Choose the original file your agent requested. Uploading it will not
        change the published site.
      </Heading>
      <Facts
        rows={[
          ["Site", p.Site],
          ["Destination", <code className={sourceValue}>{p.Path}</code>],
          ["Expected size", `${p.Size} bytes`],
        ]}
      />
      <details className="rounded-lg border bg-card px-4 py-3">
        <summary className="cursor-pointer font-medium">
          Verify the file checksum
        </summary>
        <p className="mt-3 text-sm">
          Expected SHA-256: <code className={sourceValue}>{p.SHA}</code>
        </p>
      </details>
      <form
        method="post"
        encType="multipart/form-data"
        className="grid min-w-0 gap-4 rounded-lg border bg-card p-5"
      >
        <input type="hidden" name="csrf" value={p.CSRF} />
        <div className="grid min-w-0 gap-2">
          <Label htmlFor="draft-upload">Select file</Label>
          <Input
            className="min-w-0"
            id="draft-upload"
            name="file"
            type="file"
            required
          />
        </div>
        <div>
          <Button type="submit">
            <FileUp aria-hidden="true" />
            Upload to draft
          </Button>
        </div>
      </form>
    </Shell>
  );
}

function Receipt({ p }: { p: ReceiptPage }) {
  return (
    <Shell>
      <div>
        <Badge variant="secondary">
          <CheckCircle2 aria-hidden="true" />
          Complete
        </Badge>
      </div>
      <Heading tag="Receipt" title={p.Title}>
        {p.Message}
      </Heading>
      <Facts
        rows={[
          ["Site", p.Site],
          ["Revision", <code className={sourceValue}>{p.Revision}</code>],
        ]}
      />
      <div className="flex flex-wrap gap-3">
        {p.Preview && (
          <Button asChild variant="outline">
            <a href={p.Preview}>
              Preview this revision <ArrowRight aria-hidden="true" />
            </a>
          </Button>
        )}
        {p.SiteURL && (
          <Button asChild>
            <a href={p.SiteURL}>
              Open the live site <ArrowRight aria-hidden="true" />
            </a>
          </Button>
        )}
      </div>
    </Shell>
  );
}

export function Public({ page }: { page: PublicPage }) {
  switch (page.screen) {
    case "landing":
      return <Landing signup={page.Signup} />;
    case "password":
      return <Password p={page} />;
    case "continue":
      return <Continue next={page.Next} />;
    case "approve":
      return <Approval p={page} />;
    case "upload":
      return <Upload p={page} />;
    case "receipt":
      return <Receipt p={page} />;
    case "separate-sign-in":
      return (
        <Shell>
          <Heading tag="Account" title="Sign in to continue">
            Keep this page open. Sign in with the account that requested this
            action, then close the new tab and reload this page to review the
            original request.
          </Heading>
          <div>
            <Button asChild>
              <a href="/auth/login" target="_blank" rel="noopener noreferrer">
                Sign in in a new tab <ExternalLink aria-hidden="true" />
              </a>
            </Button>
          </div>
          <p className="text-sm text-muted-foreground">
            If the link expires, request a new one from your agent.
          </p>
        </Shell>
      );
    case "error":
      return (
        <Shell>
          <Heading tag="Shoowcase" title={page.Title}>
            {page.Message}
          </Heading>
          <div>
            <Button asChild variant="outline">
              <a href="/">
                <ArrowLeft aria-hidden="true" />
                Back to workspace
              </a>
            </Button>
          </div>
        </Shell>
      );
  }
}
