
import ExcelJS from 'exceljs';

const workbook = new ExcelJS.Workbook();
const sheet = workbook.addWorksheet('My Sheet');

sheet.columns = [
    { header: 'Id', key: 'id', width: 10 },
    { header: 'Name', key: 'name', width: 32 },
    { header: 'D.O.B.', key: 'dob', width: 10, outlineLevel: 1 }
];

sheet.addRow({ id: 1, name: 'John Doe', dob: new Date(1970, 1, 1) });
sheet.addRow({ id: 2, name: 'Jane Doe', dob: new Date(1965, 1, 7) });

await workbook.xlsx.writeFile('test/fixtures/sample.xlsx');
console.log('Created test/fixtures/sample.xlsx');
