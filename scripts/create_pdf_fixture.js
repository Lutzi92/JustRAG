
const fs = require('fs');

const pdfBase64 = 'JVBERi0xLgoxIDAgb2JqPDwvUGFnZXMgMiAwIFI+PmVuZG9iagoyIDAgb2JqPDwvS2lkc1szIDAgUl0vQ291bnQgMT4+ZW5kb2JqCjMgMCBvYmo8PC9QYXJlbnQgMiAwIFI+PmVuZG9iagp0cmFpbGVyIDw8L1Jvb3QgMSAwIFI+Pg==';
const buffer = Buffer.from(pdfBase64, 'base64');

fs.writeFileSync('test/fixtures/sample.pdf', buffer);
console.log('Created test/fixtures/sample.pdf');
