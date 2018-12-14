import { Component, Input, OnChanges, ViewChild } from '@angular/core';
import * as JsDiff from 'diff';

export class Mode {
    static UNIFIED = 'unified';
    static SPLIT = 'split';
}

@Component({
    selector: 'app-diff-item',
    templateUrl: './diff.item.html',
    styleUrls: ['./diff.item.scss']
})
export class DiffItemComponent implements OnChanges {
    @ViewChild('codeLeft') codeLeft;
    @ViewChild('codeRight') codeRight;
    @Input() original: string;
    @Input() updated: string;
    @Input() mode: Mode = Mode.UNIFIED;
    @Input() type = 'text/plain';
    diff: any[];
    contentLeft: string;
    contentRight: string;
    codeMirrorConfig: any;

    constructor() {
        this.codeMirrorConfig = {
            matchBrackets: true,
            autoCloseBrackets: true,
            mode: this.type,
            lineWrapping: true,
            autoRefresh: true,
            readOnly: true,
            lineNumbers: true
        };
    }

    onCodeLeftChange() {
        if (this.codeLeft.instance) {
            let index = 0;
            this.diff.forEach(part => {
                if ((this.mode === Mode.UNIFIED && part.added) || part.removed) {
                    let start = index;
                    let end = start + part.count;
                    for (let i = start; i < end; i++) {
                        this.codeLeft.instance.doc.addLineClass(i, 'background', part.added ? 'codeAdded' : 'codeRemoved');
                    }
                }
                index += part.count;
            });
        }
    }

    onCodeRightChange() {
        if (this.mode === Mode.SPLIT && this.codeRight.instance) {
            let index = 0;
            this.diff.forEach(part => {
                if (part.added) {
                    let start = index;
                    let end = start + part.count;
                    for (let i = start; i < end; i++) {
                        this.codeRight.instance.doc.addLineClass(i, 'background', 'codeAdded');
                    }
                }
                index += part.count;
            });
        }
    }

    ngOnChanges() {
        if (this.original && this.updated) {
            this.refresh();
        }
        this.codeMirrorConfig.mode = this.type;
    }

    refresh() {
        let original = this.original || '';
        if (original === 'null') {
            original = '';
        }

        let updated = this.updated;
        if (this.type === 'application/json') {
            try {
                original = JSON.stringify(JSON.parse(original), null, 2);
            } catch (e) { }
            try {
                updated = JSON.stringify(JSON.parse(updated), null, 2);
            } catch (e) { }
        }

        let diff = JsDiff.diffLines(original, updated);

        if (!Array.isArray(diff)) {
            return;
        }

        this.diff = diff;

        if (this.mode === Mode.UNIFIED) {
            this.contentLeft = diff.reduce((v, part) => {
                return v + part.value;
            }, '');
            return;
        }

        this.contentLeft = diff.reduce((v, part) => {
            return v + (part.added ? '\n'.repeat(part.count) : part.value);
        }, '');

        this.contentRight = diff.reduce((v, part) => {
            return v + (part.removed ? '\n'.repeat(part.count) : part.value);
        }, '');
    }
}