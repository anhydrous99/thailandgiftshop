import { expect, type APIRequestContext } from '@playwright/test';

export type CapturedEmailMessage = {
  id: string;
  to: string;
  subject: string;
  text: string;
  html: string;
  kind: string;
  eventKey: string;
  createdAt: string;
};

export type CapturedEmailCriteria = {
  to?: string;
  kind?: string;
  subject?: string | RegExp;
};

export type ClearCapturedEmailOptions = {
  to?: string;
};

export async function clearCapturedEmails(request: APIRequestContext, options: ClearCapturedEmailOptions = {}): Promise<void> {
  const path = new URL('/__test/emails/clear', 'http://127.0.0.1:8080');
  if (options.to !== undefined) {
    path.searchParams.set('to', options.to);
  }
  const response = await request.post(`${path.pathname}${path.search}`);

  expect(response.status()).toBe(204);
}

export async function listCapturedEmails(request: APIRequestContext): Promise<CapturedEmailMessage[]> {
  const response = await request.get('/__test/emails');

  expect(response.status()).toBe(200);
  const messages = await response.json();
  expect(Array.isArray(messages)).toBe(true);
  return messages.map(assertCapturedEmailMessage);
}

export function findCapturedEmail(
  messages: CapturedEmailMessage[],
  criteria: CapturedEmailCriteria,
): CapturedEmailMessage | null {
  return messages.find((message) => matchesCapturedEmail(message, criteria)) ?? null;
}

export function extractResetLink(message: CapturedEmailMessage, baseURL = 'http://127.0.0.1:8080'): string {
  const base = new URL(baseURL);
  const candidates = `${message.text}\n${message.html}`.match(/(?:https?:\/\/[^\s"'<>]+|\/account\/password-reset[^\s"'<>]*)/g) ?? [];

  for (const candidate of candidates) {
    const link = new URL(candidate, base);
    if (link.origin === base.origin && link.pathname.startsWith('/account/password-reset')) {
      return link.toString();
    }
  }

  throw new Error(`No same-origin password reset link found in captured email ${message.id}`);
}

function matchesCapturedEmail(message: CapturedEmailMessage, criteria: CapturedEmailCriteria): boolean {
  if (criteria.to !== undefined && message.to !== criteria.to) {
    return false;
  }
  if (criteria.kind !== undefined && message.kind !== criteria.kind) {
    return false;
  }
  if (criteria.subject instanceof RegExp) {
    return criteria.subject.test(message.subject);
  }
  if (criteria.subject !== undefined && message.subject !== criteria.subject) {
    return false;
  }
  return true;
}

function assertCapturedEmailMessage(message: unknown): CapturedEmailMessage {
  expect(message).toEqual({
    id: expect.any(String),
    to: expect.any(String),
    subject: expect.any(String),
    text: expect.any(String),
    html: expect.any(String),
    kind: expect.any(String),
    eventKey: expect.any(String),
    createdAt: expect.any(String),
  });
  return message as CapturedEmailMessage;
}
