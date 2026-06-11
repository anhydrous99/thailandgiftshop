import { expect, test } from '@playwright/test';

import { clearCapturedEmails, findCapturedEmail, listCapturedEmails } from './email-capture';

test('fake email capture endpoint clears and lists the outbox', async ({ request }) => {
  await clearCapturedEmails(request, { to: 'nobody@example.test' });

  const messages = await listCapturedEmails(request);

  expect(findCapturedEmail(messages, { to: 'nobody@example.test' })).toBeNull();
});
