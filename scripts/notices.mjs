import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';

const root=path.resolve(import.meta.dirname,'..');
const chunks=['Relay Lab third-party license notices\nGenerated from pinned Go modules and installed frontend packages.\n'];
const accepted=/^(?:licen[sc]e|copying|notice)(?:[.-].*)?$/i;
function licenses(dir,label){if(!dir||!fs.existsSync(dir))return;for(const name of fs.readdirSync(dir)){const file=path.join(dir,name);if(accepted.test(name)&&fs.statSync(file).isFile()){const text=fs.readFileSync(file,'utf8');chunks.push(`\n${'='.repeat(72)}\n${label} — ${name}\n${'='.repeat(72)}\n${text}\n`);}}}
const mods=execFileSync('go',['list','-m','-f','{{.Path}}\t{{.Version}}\t{{.Dir}}','all'],{cwd:root,encoding:'utf8'}).trim().split(/\r?\n/);
for(const line of mods){const [name,version,dir]=line.split('\t');if(name!=='relaylab')licenses(dir,`${name} ${version}`);}
const goroot=execFileSync('go',['env','GOROOT'],{cwd:root,encoding:'utf8'}).trim();licenses(goroot,'Go standard library');
const visited=new Set();
function packageDir(dir){if(!fs.existsSync(dir))return;const manifest=path.join(dir,'package.json');if(fs.existsSync(manifest)){const pkg=JSON.parse(fs.readFileSync(manifest,'utf8'));const key=`${pkg.name}@${pkg.version}`;if(!visited.has(key)){visited.add(key);licenses(dir,key);}}const nested=path.join(dir,'node_modules');if(fs.existsSync(nested))walk(nested);}
function walk(dir){for(const name of fs.readdirSync(dir)){if(name.startsWith('.'))continue;const child=path.join(dir,name);if(!fs.statSync(child).isDirectory())continue;if(name.startsWith('@')){for(const scoped of fs.readdirSync(child))packageDir(path.join(child,scoped));}else packageDir(child);}}
walk(path.join(root,'web','node_modules'));
fs.mkdirSync(path.join(root,'release'),{recursive:true});const destination=path.join(root,'release','THIRD_PARTY_NOTICES.txt');fs.writeFileSync(destination,chunks.join(''));console.log(`License notices generated: ${destination} (${fs.statSync(destination).size} bytes)`);
