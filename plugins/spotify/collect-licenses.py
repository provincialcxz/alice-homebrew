"""Write licenses/rust-dependencies.txt and copy every Rust dependency's license
files into licenses/rust/. MIT, Apache and BSD require the text to travel with
the binary. Run after the librespot build; works on Linux and Windows.

Usage: collect-licenses.py LIBRESPOT_SOURCE_DIR TOOLCHAIN
"""
from pathlib import Path
import os,shutil,subprocess,sys
source=Path(sys.argv[1]);toolchain=sys.argv[2]
plugin=Path(__file__).resolve().parent
licenses=plugin/'licenses';rust=licenses/'rust'
tree=subprocess.run(['cargo','+'+toolchain,'tree','--locked','--target','aarch64-unknown-linux-musl','--no-default-features',
 '--features','with-libmdns,rustls-tls-webpki-roots','--edges','normal','--prefix','none','--format','{p} {l}'],
 cwd=source,capture_output=True,text=True,check=True).stdout
lines=sorted({l.replace(' (*)','').strip() for l in tree.splitlines() if l.strip()})
(licenses/'rust-dependencies.txt').write_text('\n'.join(lines)+'\n',encoding='utf-8')
registry=Path(os.environ.get('CARGO_HOME',Path.home()/'.cargo'))/'registry'/'src'
shutil.rmtree(rust,ignore_errors=True);rust.mkdir(parents=True)
missing=[]
def common_text(spdx):
 # Prefer MPL-2.0, then MIT, among licenses offered in an OR expression.
 for choice in ('MPL-2.0','MIT'):
  if choice in spdx.replace('/',' OR ').split(' OR ') or spdx==choice:return choice
 return None
for line in lines:
 name,version=line.split()[:2];version=version.lstrip('v')
 if name=='librespot' or name.startswith('librespot-'):continue # one license, shipped as librespot-*-LICENSE
 found=[d for d in registry.glob(f'*/{name}-{version}') if d.is_dir()]
 files=[f for d in found[:1] for f in sorted(d.iterdir()) if f.is_file() and f.name.upper().startswith(('LICENSE','LICENCE','COPYING','NOTICE','UNLICENSE'))]
 for f in files:shutil.copyfile(f,rust/f'{name}-{version}-{f.name}')
 if files:continue
 # A few crates publish no license file. Record their SPDX expression and
 # authors next to the full text in licenses/common/, and where the exact
 # source is (MPL-2.0 requires the covered source to stay available).
 meta=(found[0]/'Cargo.toml').read_text(encoding='utf-8') if found else ''
 authors=next((l.split('=',1)[1].strip() for l in meta.splitlines() if l.startswith('authors')),'see the crate')
 spdx=line.split(None,2)[2] if len(line.split())>2 else 'unknown'
 text=common_text(spdx)
 if not text:missing.append(line);continue
 (rust/f'{name}-{version}-LICENSE-NOTICE.txt').write_text(f"""{name} {version}
License: {spdx} (this distribution uses {text})
Authors: {authors}
Source: https://crates.io/crates/{name}/{version}
Full license text: ../common/{text}.txt
""",encoding='utf-8')
print(f'{len(lines)} dependencies, {len(list(rust.iterdir()))} license files')
if missing:sys.exit('No usable license for: '+'; '.join(missing))
