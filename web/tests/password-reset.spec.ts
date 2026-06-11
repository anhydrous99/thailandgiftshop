import { expect, test, type Page, type TestInfo } from '@playwright/test';

import { clearCapturedEmails, extractResetLink, findCapturedEmail, listCapturedEmails } from './email-capture';

const oldPassword = 'playwright-demo-password';
const newPassword = 'playwright-reset-password';
const resetSentCopy = 'If an account uses that email, we sent a reset link.';
const invalidResetCopy = 'This reset link is invalid or expired.';

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

async function signUp(page: Page, email: string) {
  await page.goto('/account/sign-up');
  await expect(page.getByTestId('signup-form')).toBeVisible();
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password (8 to 72 characters)').fill(oldPassword);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page).toHaveURL(/\/account$/);
  await expect(page.getByRole('heading', { name: 'Your account', level: 1 })).toBeVisible();
}

async function requestPasswordReset(page: Page, email: string) {
  await page.goto('/account/password-reset');
  const form = page.getByTestId('password-reset-request-form');
  await expect(form).toBeVisible();
  await form.getByLabel('Email').fill(email);
  await form.getByRole('button', { name: 'Send reset link' }).click();
  await expect(page).toHaveURL(/\/account\/password-reset\?sent=1$/);
  await expect(page.getByRole('status')).toContainText(resetSentCopy);
}

async function signIn(page: Page, email: string, password: string) {
  const form = page.getByTestId('signin-form');
  await expect(form).toBeVisible();
  await form.getByLabel('Email').fill(email);
  await form.getByLabel('Password').fill(password);
  await form.getByRole('button', { name: 'Sign in' }).click();
}

test.describe('without JavaScript', () => {
  test.describe.configure({ mode: 'serial' });

  test.use({
    javaScriptEnabled: false,
    viewport: { width: 1280, height: 1800 },
  });

  test('known customer can reset password once from captured fake email', async ({ page, request }, testInfo) => {
    const email = uniqueEmail(testInfo, 'password-reset');

    await signUp(page, email);
    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page).toHaveURL(/\/$/);

    await clearCapturedEmails(request, { to: email });
    await requestPasswordReset(page, email);

    const messages = await listCapturedEmails(request);
    const resetMessages = messages.filter((message) => message.to === email && message.kind === 'password_reset');
    expect(resetMessages).toHaveLength(1);
    const resetMessage = findCapturedEmail(messages, {
      to: email,
      kind: 'password_reset',
      subject: /reset/i,
    });
    expect(resetMessage).not.toBeNull();
    const resetLink = extractResetLink(resetMessage!);

    await page.goto(resetLink);
    const confirmForm = page.getByTestId('password-reset-confirm-form');
    await expect(confirmForm).toBeVisible();
    await confirmForm.getByLabel('New password (8 to 72 characters)').fill(newPassword);
    await confirmForm.getByRole('button', { name: 'Reset password' }).click();

    await expect(page).toHaveURL(/\/account\/sign-in\?password_reset=1$/);
    await expect(page.getByRole('status')).toContainText('Password reset. Sign in with your new password.');

    await signIn(page, email, oldPassword);
    await expect(page.getByRole('alert')).toContainText('Invalid email or password.');
    await expect(page.getByTestId('signin-form')).toBeVisible();

    await signIn(page, email, newPassword);
    await expect(page).toHaveURL(/\/account$/);
    await expect(page.getByText(`Signed in as ${email}`)).toBeVisible();

    await page.goto(resetLink);
    await expect(page.getByRole('alert')).toContainText(invalidResetCopy);
    await expect(page.getByTestId('password-reset-confirm-form')).toHaveCount(0);
  });

  test('unknown reset request shows generic success and sends no email', async ({ page, request }, testInfo) => {
    const unknownEmail = uniqueEmail(testInfo, 'missing-reset-customer');

    await clearCapturedEmails(request, { to: unknownEmail });
    await requestPasswordReset(page, unknownEmail);

    const messages = await listCapturedEmails(request);
    const resetMessage = findCapturedEmail(messages, {
      to: unknownEmail,
      kind: 'password_reset',
      subject: /reset/i,
    });
    expect(resetMessage).toBeNull();
  });
});
